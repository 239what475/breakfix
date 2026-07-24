package gateway

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"log/slog"
)

const (
	terminalHeartbeatTimeout    = 3 * time.Minute
	terminalConnectionRetention = 24 * time.Hour
)

func newGatewayInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "gateway"
	}
	return fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
}

// StartLearningCleanup bounds durable terminal activity records after a
// Gateway crash or a lost close frame. Environment idle leases remain owned by
// the controllers and are not modified here.
func (h *Handler) StartLearningCleanup(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	cleanup := func() {
		now := time.Now().UTC()
		if err := h.db.CleanupTerminalActivity(ctx, now.Add(-terminalHeartbeatTimeout), now); err != nil {
			slog.Warn("cleanup terminal activity", "err", err)
			return
		}
		if err := h.db.DeleteClosedTerminalConnections(ctx, now.Add(-terminalConnectionRetention)); err != nil {
			slog.Warn("cleanup terminal connection retention", "err", err)
		}
	}
	cleanup()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
}
