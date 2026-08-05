package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ReadyProbe func(context.Context) error

type Config struct {
	Port         int
	Component    string
	Ready        ReadyProbe
	Capabilities []Capability
}

// Capability is a live, task-specific dependency check. Capabilities are not
// Kubernetes readiness gates: a Node provider outage must not prevent the
// Runtime Worker from processing an unrelated K8s runtime action.
type Capability struct {
	Name  string
	Ready ReadyProbe
}

var componentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Run owns the health listener and worker loop as one process lifecycle.
func Run(parent context.Context, config Config, work func(context.Context) error) error {
	if err := validateConfig(config, work); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", config.Port))
	if err != nil {
		return fmt.Errorf("listen for worker health: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	handler := newHandler(config)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveDone <- err
	}()
	workDone := make(chan error, 1)
	go func() { workDone <- work(ctx) }()

	var result error
	select {
	case <-parent.Done():
	case err := <-serveDone:
		if err != nil {
			result = fmt.Errorf("worker health server: %w", err)
		}
	case err := <-workDone:
		if err != nil {
			result = err
		} else if parent.Err() == nil {
			result = errors.New("worker loop stopped unexpectedly")
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	return result
}

func validateConfig(config Config, work func(context.Context) error) error {
	if config.Port <= 0 || !componentNamePattern.MatchString(config.Component) || work == nil {
		return errors.New("worker health requires a port, component, and work loop")
	}
	seen := make(map[string]struct{}, len(config.Capabilities))
	for _, capability := range config.Capabilities {
		if !componentNamePattern.MatchString(capability.Name) || capability.Ready == nil {
			return errors.New("worker health capability requires a name and readiness probe")
		}
		if _, duplicate := seen[capability.Name]; duplicate {
			return fmt.Errorf("worker health capability %q is duplicated", capability.Name)
		}
		seen[capability.Name] = struct{}{}
	}
	return nil
}

func newHandler(config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		if err := checkReady(request.Context(), config.Ready); err != nil {
			http.Error(writer, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	capabilities := make(map[string]ReadyProbe, len(config.Capabilities))
	for _, capability := range config.Capabilities {
		capabilities[capability.Name] = capability.Ready
	}
	mux.HandleFunc("/capabilities/", func(writer http.ResponseWriter, request *http.Request) {
		name := strings.TrimPrefix(request.URL.Path, "/capabilities/")
		probe, found := capabilities[name]
		if !found || name == "" || strings.Contains(name, "/") {
			http.NotFound(writer, request)
			return
		}
		if err := checkReady(request.Context(), probe); err != nil {
			http.Error(writer, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/metrics", func(writer http.ResponseWriter, request *http.Request) {
		ready := 1
		if checkReady(request.Context(), config.Ready) != nil {
			ready = 0
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(writer, "# TYPE breakfix_worker_up gauge\nbreakfix_worker_up{component=%q} 1\n", config.Component)
		_, _ = fmt.Fprintf(writer, "# TYPE breakfix_worker_ready gauge\nbreakfix_worker_ready{component=%q} %d\n", config.Component, ready)
		_, _ = fmt.Fprint(writer, "# TYPE breakfix_worker_capability_ready gauge\n")
		for _, capability := range config.Capabilities {
			available := 1
			if checkReady(request.Context(), capability.Ready) != nil {
				available = 0
			}
			_, _ = fmt.Fprintf(writer, "breakfix_worker_capability_ready{component=%q,capability=%q} %d\n", config.Component, capability.Name, available)
		}
	})
	return mux
}

func checkReady(parent context.Context, probe ReadyProbe) error {
	if probe == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return probe(ctx)
}
