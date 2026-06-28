package server

import (
	"log/slog"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
)

// CooldownManager starts a 5-minute draining timer when a user disconnects.
// If the user reconnects (ping) before expiry the timer is cancelled.
type CooldownManager struct {
	db        *db.DB
	k8s       *k8s.Client
	mu        sync.Mutex
	timers    map[string]*time.Timer
	cleanupFn func(string)
}

func NewCooldownManager(database *db.DB, client *k8s.Client, cleanupFn func(string)) *CooldownManager {
	return &CooldownManager{
		db:        database,
		k8s:       client,
		timers:    make(map[string]*time.Timer),
		cleanupFn: cleanupFn,
	}
}

func (m *CooldownManager) SetCleanup(fn func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupFn = fn
}

func (m *CooldownManager) StartDraining(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[instanceID]; ok {
		t.Stop()
	}
	if err := m.db.UpdateInstanceStatus(instanceID, "draining"); err != nil {
		slog.Error("failed to update instance status", "err", err, "instance", instanceID, "status", "draining")
	}
	slog.Debug("instance draining", "instance", instanceID, "cooldown", "5min")
	m.timers[instanceID] = time.AfterFunc(5*time.Minute, func() {
		m.destroy(instanceID)
	})
}

func (m *CooldownManager) Cancel(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[instanceID]; ok {
		t.Stop()
		delete(m.timers, instanceID)
	}
}

func (m *CooldownManager) destroy(instanceID string) {
	m.mu.Lock()
	delete(m.timers, instanceID)
	m.mu.Unlock()

	slog.Info("cooldown expired, destroying instance", "instance", instanceID)
	m.cleanupFn(instanceID)
}

func (m *CooldownManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.timers {
		t.Stop()
		delete(m.timers, id)
	}
}
