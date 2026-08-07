package server

import (
	"context"
	"sync"
	"testing"
)

func TestRuntimeCloseWaitsForServicesBeforeDependencies(t *testing.T) {
	lifecycle := newServiceLifecycle()
	started := make(chan struct{})
	stopped := make(chan struct{})
	var mu sync.Mutex
	order := make([]string, 0, 2)
	lifecycle.start("test", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		mu.Lock()
		order = append(order, "service")
		mu.Unlock()
		close(stopped)
		return nil
	})
	<-started
	runtime := &Runtime{
		services: lifecycle,
		closeDependencies: func() {
			select {
			case <-stopped:
			default:
				t.Error("dependencies closed before background service stopped")
			}
			mu.Lock()
			order = append(order, "dependencies")
			mu.Unlock()
		},
	}
	runtime.Close()
	runtime.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "service" || order[1] != "dependencies" {
		t.Fatalf("close order = %#v", order)
	}
}
