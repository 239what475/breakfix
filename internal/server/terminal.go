package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"log/slog"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/gorilla/websocket"
)

type wsMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

const (
	terminalHeartbeatInterval = time.Minute
	terminalTicketTTL         = time.Minute
)

type terminalSocketLifecycle struct {
	open      func() error
	heartbeat func()
	close     func()
}

type NodeTerminalProvider interface {
	ExecNodePTY(context.Context, incus.ExecNodePTYRequest) error
	CloseNodePTYWindow(context.Context, incus.CloseNodePTYWindowRequest) error
	ExecNode(context.Context, incus.ExecNodeRequest) (incus.ExecNodeResult, error)
}

// NodeProviderReadiness is intentionally separate from terminal operations.
// The concrete Incus client exposes this so Server can report whether the
// Node runtime is currently usable without broadening the terminal contract.
type NodeProviderReadiness interface {
	Preflight(context.Context) (incus.PreflightResult, error)
}

type terminalStream func(context.Context, io.Reader, io.Writer, <-chan environment.Size) error

func wsUpgrade(w http.ResponseWriter, r *http.Request, uiOrigin string, env *activeEnvironment, runtime *environmentRuntimeAdapter, cooldownMin int, stream terminalStream, lifecycle terminalSocketLifecycle) {
	upgrader := terminalUpgrader(uiOrigin)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()
	terminalCtx, cancelTerminal := context.WithCancel(r.Context())
	defer cancelTerminal()
	if lifecycle.open != nil {
		if err := lifecycle.open(); err != nil {
			slog.Error("open terminal activity", "err", err, "environment", env.Name)
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "unable to start terminal"), time.Now().Add(time.Second))
			return
		}
	}
	stopHeartbeat := make(chan struct{})
	defer func() {
		close(stopHeartbeat)
		if lifecycle.close != nil {
			lifecycle.close()
		}
	}()
	if lifecycle.heartbeat != nil {
		conn.SetPongHandler(func(string) error {
			lifecycle.heartbeat()
			return nil
		})
		go keepTerminalConnectionAlive(terminalCtx, conn, stopHeartbeat)
	}

	resizeCh := make(chan environment.Size, 4)
	stdinR, stdinW := io.Pipe()
	output := &wsWriter{conn: conn}

	// Read from WebSocket → pipe to PTY stdin
	go func() {
		defer func() { _ = stdinW.Close() }()
		defer cancelTerminal()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m wsMsg
			if err := json.Unmarshal(msg, &m); err != nil {
				if _, writeErr := stdinW.Write(msg); writeErr != nil {
					return
				}
				continue
			}
			if m.Type == "resize" {
				select {
				case resizeCh <- environment.Size{Width: uint16(m.Cols), Height: uint16(m.Rows)}:
				default:
				}
				continue
			}
			if m.Type == "data" && len(m.Data) > 0 {
				if _, writeErr := stdinW.Write([]byte(m.Data)); writeErr != nil {
					return
				}
			}
		}
	}()

	stopLease := make(chan struct{})
	defer close(stopLease)
	idleTTL := environmentIdleTTL(env, time.Duration(cooldownMin)*time.Minute)
	if env.Phase == breakfixv1.EnvironmentReady && runtime != nil {
		go keepEnvironmentLeaseAlive(terminalCtx, runtime, env.Name, idleTTL, stopLease)
	}
	if stream == nil {
		err = fmt.Errorf("terminal stream is not configured")
	} else {
		err = stream(terminalCtx, stdinR, output, resizeCh)
	}

	if err != nil {
		slog.Debug("pty session ended", "err", err)
	}
}

func terminalUpgrader(expectedOrigin string) websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return terminalOriginAllowed(r.Header.Get("Origin"), expectedOrigin)
		},
	}
}

func terminalOriginAllowed(actual, expected string) bool {
	actualURL, actualErr := url.ParseRequestURI(strings.TrimSpace(actual))
	expectedURL, expectedErr := url.ParseRequestURI(strings.TrimSpace(expected))
	if actualErr != nil || expectedErr != nil || actualURL == nil || expectedURL == nil {
		return false
	}
	return actualURL.Scheme == expectedURL.Scheme && actualURL.Host == expectedURL.Host && actualURL.User == nil && actualURL.RawQuery == "" && actualURL.Fragment == "" && (actualURL.Path == "" || actualURL.Path == "/")
}

func keepTerminalConnectionAlive(ctx context.Context, conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(terminalHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			// WriteControl is safe alongside the PTY data writer and gives the
			// server a real liveness signal even while a browser tab is hidden.
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		}
	}
}

func newTerminalConnectionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read terminal connection id: %w", err)
	}
	return "term-" + hex.EncodeToString(raw[:]), nil
}

func newTerminalTicket() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read terminal ticket: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func terminalTicketHash(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(sum[:])
}

// terminalConnectionTracker treats all terminal windows of one environment as
// one interactive session. The settle delay prevents a tab switch from being
// interpreted as an abandoned environment during WebSocket handover.
type terminalConnectionTracker struct {
	mu          sync.Mutex
	active      map[string]int
	pending     map[string]*time.Timer
	settleDelay time.Duration
}

func newTerminalConnectionTracker(settleDelay time.Duration) *terminalConnectionTracker {
	return &terminalConnectionTracker{
		active:      make(map[string]int),
		pending:     make(map[string]*time.Timer),
		settleDelay: settleDelay,
	}
}

func (t *terminalConnectionTracker) open(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if timer := t.pending[key]; timer != nil {
		timer.Stop()
		delete(t.pending, key)
	}
	t.active[key]++
}

func (t *terminalConnectionTracker) close(key string, onEmpty func()) {
	t.mu.Lock()
	if t.active[key] > 1 {
		t.active[key]--
		t.mu.Unlock()
		return
	}
	delete(t.active, key)
	if timer := t.pending[key]; timer != nil {
		timer.Stop()
	}
	t.pending[key] = time.AfterFunc(t.settleDelay, func() {
		t.mu.Lock()
		if t.active[key] != 0 {
			t.mu.Unlock()
			return
		}
		delete(t.pending, key)
		t.mu.Unlock()
		onEmpty()
	})
	t.mu.Unlock()
}

var terminalWindowName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func parseTerminalWindow(raw string) (string, error) {
	if raw == "" {
		return "shell-1", nil
	}
	if !terminalWindowName.MatchString(raw) {
		return "", fmt.Errorf("invalid terminal window")
	}
	return raw, nil
}

func terminalSessionName(environmentUID string) string {
	sum := sha256.Sum256([]byte(environmentUID))
	return "bf-" + hex.EncodeToString(sum[:10])
}

func keepEnvironmentLeaseAlive(ctx context.Context, runtime *environmentRuntimeAdapter, environmentName string, idleTTL time.Duration, stop <-chan struct{}) {
	if runtime == nil || idleTTL <= 0 {
		return
	}
	interval := idleTTL / 2
	if interval < time.Minute {
		interval = time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		renewCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := runtime.renewActivity(renewCtx, environmentName, nowActivity())
		cancel()
		if err != nil {
			slog.Debug("failed to renew environment lease", "environment", environmentName, "err", err)
		}

		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type wsWriter struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	readyOnce sync.Once
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var readyErr error
	w.readyOnce.Do(func() {
		readyErr = w.writeLocked(wsMsg{Type: "ready"})
	})
	if readyErr != nil {
		return 0, readyErr
	}
	if err := w.writeLocked(wsMsg{Type: "data", Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsWriter) writeLocked(message wsMsg) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return w.conn.WriteMessage(websocket.TextMessage, payload)
}

// ensure io import
var _ io.Reader
