package gateway

import (
	"encoding/json"
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
	Data []byte `json:"data,omitempty"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

func wsUpgrade(w http.ResponseWriter, r *http.Request, inst *breakfixv1.Instance, k8sClient *k8s.Client, crdNamespace string, cooldownMin int) {
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
			if len(m.Data) > 0 {
				stdinW.Write(m.Data)
			}
		}
	}()

	err = k8sClient.ExecPTY(stdinR, &wsWriter{conn: conn}, &wsWriter{conn: conn}, resizeCh, inst.Status.Namespace, inst.Status.PodName)

	// On disconnect, start cooldown
	if inst.Status.Phase == breakfixv1.InstanceRunning {
		drainTime := metav1.NewTime(time.Now().Add(time.Duration(cooldownMin) * time.Minute))
		inst.Status.Phase = breakfixv1.InstanceDraining
		inst.Status.CooldownUntil = &drainTime
		if _, updateErr := k8sClient.UpdateInstanceStatus(r.Context(), crdNamespace, inst); updateErr != nil {
			slog.Error("failed to start draining", "err", updateErr, "instance", inst.Name)
		}
		slog.Info("instance draining", "instance", inst.Name, "cooldown_min", cooldownMin)
	}

	if err != nil {
		slog.Debug("pty session ended", "err", err)
	}
}

type wsWriter struct {
	conn *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	msg, _ := json.Marshal(wsMsg{Type: "data", Data: p})
	if err := w.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ensure io import
var _ io.Reader
