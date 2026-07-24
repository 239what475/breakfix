package server

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestParseTerminalWindow(t *testing.T) {
	window, err := parseTerminalWindow("")
	if err != nil || window != "shell-1" {
		t.Fatalf("default window = %q, %v", window, err)
	}

	for _, name := range []string{"shell-1", "logs", "shell-12"} {
		if _, err := parseTerminalWindow(name); err != nil {
			t.Fatalf("expected %q to be valid: %v", name, err)
		}
	}
	for _, name := range []string{"Shell", "1-shell", "shell_1", "shell;rm", "shell/window"} {
		if _, err := parseTerminalWindow(name); err == nil {
			t.Fatalf("expected %q to be invalid", name)
		}
	}
}

func TestTerminalConnectionTrackerWaitsForLastConnection(t *testing.T) {
	tracker := newTerminalConnectionTracker(20 * time.Millisecond)
	var drained atomic.Int32
	drain := func() { drained.Add(1) }

	tracker.open("container/demo")
	tracker.open("container/demo")
	tracker.close("container/demo", drain)
	time.Sleep(40 * time.Millisecond)
	if drained.Load() != 0 {
		t.Fatal("closing one of two terminal windows must not drain the environment")
	}

	tracker.close("container/demo", drain)
	time.Sleep(40 * time.Millisecond)
	if drained.Load() != 1 {
		t.Fatalf("expected one drain after the final terminal closed, got %d", drained.Load())
	}
}

func TestTerminalConnectionTrackerCancelsPendingDrainOnReconnect(t *testing.T) {
	tracker := newTerminalConnectionTracker(40 * time.Millisecond)
	var drained atomic.Int32
	drain := func() { drained.Add(1) }

	tracker.open("container/demo")
	tracker.close("container/demo", drain)
	time.Sleep(10 * time.Millisecond)
	tracker.open("container/demo")
	time.Sleep(60 * time.Millisecond)
	if drained.Load() != 0 {
		t.Fatal("reconnecting before the settle delay must cancel draining")
	}
}
