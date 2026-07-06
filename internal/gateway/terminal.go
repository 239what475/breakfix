package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
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

func wsUpgrade(w http.ResponseWriter, r *http.Request, env *activeEnvironment, k8sClient *k8s.Client, crdNamespace string, cooldownMin int) {
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
	err = k8sClient.ExecPTY(stdinR, &wsWriter{conn: conn}, &wsWriter{conn: conn}, resizeCh, env.Namespace, env.WorkspacePod, sessionName)

	// On disconnect, mark the environment with an expiry deadline for auto-reclaim.
	if env.Phase == "Ready" {
		expiresAt := metav1.NewTime(time.Now().Add(time.Duration(cooldownMin) * time.Minute))
		switch env.Runtime {
		case challenge.RuntimeContainer:
			current, getErr := k8sClient.GetContainerEnvironment(r.Context(), crdNamespace, env.Name)
			if getErr == nil {
				current.Status.Phase = "Draining"
				current.Status.ExpiresAt = &expiresAt
				if _, updateErr := k8sClient.UpdateContainerEnvironmentStatus(r.Context(), crdNamespace, current); updateErr != nil {
					slog.Error("failed to start draining", "err", updateErr, "environment", env.Name)
				}
			}
		case challenge.RuntimeVCluster:
			current, getErr := k8sClient.GetVClusterEnvironment(r.Context(), crdNamespace, env.Name)
			if getErr == nil {
				current.Status.Phase = "Draining"
				current.Status.ExpiresAt = &expiresAt
				if _, updateErr := k8sClient.UpdateVClusterEnvironmentStatus(r.Context(), crdNamespace, current); updateErr != nil {
					slog.Error("failed to start draining", "err", updateErr, "environment", env.Name)
				}
			}
		}
		slog.Info("environment draining", "environment", env.Name, "expires_in_min", cooldownMin)
	}

	if err != nil {
		slog.Debug("pty session ended", "err", err)
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
