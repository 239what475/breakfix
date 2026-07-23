package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
	"log/slog"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

func wsUpgrade(w http.ResponseWriter, r *http.Request, env *activeEnvironment, k8sClient *k8s.Client, runtime *environmentRuntimeAdapter, cooldownMin int, windowName string, onOpen, onClose func()) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade", "err", err)
		return
	}
	defer conn.Close()
	if onOpen != nil {
		onOpen()
	}
	if onClose != nil {
		defer onClose()
	}

	resizeCh := make(chan remotecommand.TerminalSize, 4)
	stdinR, stdinW := io.Pipe()

	// Read from WebSocket → pipe to PTY stdin
	go func() {
		defer stdinW.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m wsMsg
			if err := json.Unmarshal(msg, &m); err != nil {
				stdinW.Write(msg)
				continue
			}
			if m.Type == "resize" {
				select {
				case resizeCh <- remotecommand.TerminalSize{Width: uint16(m.Cols), Height: uint16(m.Rows)}:
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

	sessionName := fmt.Sprintf("breakfix-%s", env.Name)
	stopLease := make(chan struct{})
	defer close(stopLease)
	idleTTL := environmentIdleTTL(env, time.Duration(cooldownMin)*time.Minute)
	if env.Phase == breakfixv1.EnvironmentReady && runtime != nil {
		go keepEnvironmentLeaseAlive(r.Context(), runtime, env.Name, idleTTL, stopLease)
	}
	err = k8sClient.ExecPTY(stdinR, &wsWriter{conn: conn}, &wsWriter{conn: conn}, resizeCh, env.Namespace, env.WorkspacePod, sessionName, windowName)

	if err != nil {
		slog.Debug("pty session ended", "err", err)
	}
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
		expiresAt := metav1.NewTime(time.Now().Add(idleTTL))
		renewCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := runtime.renewLease(renewCtx, environmentName, expiresAt)
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
	conn *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	msg, _ := json.Marshal(wsMsg{Type: "data", Data: string(p)})
	if err := w.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ensure io import
var _ io.Reader
