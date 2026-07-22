package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func wsUpgrade(w http.ResponseWriter, r *http.Request, env *activeEnvironment, k8sClient *k8s.Client, runtime *environmentRuntimeAdapter, cooldownMin int) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade", "err", err)
		return
	}
	defer conn.Close()

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
	drainGrace := environmentDrainGracePeriod(env, idleTTL)
	if env.Phase == breakfixv1.EnvironmentReady && runtime != nil {
		go keepEnvironmentLeaseAlive(r.Context(), runtime, env.Name, idleTTL, stopLease)
	}
	err = k8sClient.ExecPTY(stdinR, &wsWriter{conn: conn}, &wsWriter{conn: conn}, resizeCh, env.Namespace, env.WorkspacePod, sessionName)

	// On disconnect, mark the environment with an expiry deadline for auto-reclaim.
	if env.Phase == breakfixv1.EnvironmentReady && runtime != nil {
		expiresAt := metav1.NewTime(time.Now().Add(drainGrace))
		if updateErr := runtime.markDraining(r.Context(), env.Name, expiresAt); updateErr != nil {
			slog.Error("failed to start draining", "err", updateErr, "environment", env.Name)
		}
		slog.Info("environment draining", "environment", env.Name, "expires_in", drainGrace.String())
	}

	if err != nil {
		slog.Debug("pty session ended", "err", err)
	}
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
