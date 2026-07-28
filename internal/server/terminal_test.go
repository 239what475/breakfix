package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestWSWriterSignalsReadyBeforeFirstTerminalData(t *testing.T) {
	messages := make(chan wsMsg, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		for range 2 {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Errorf("read websocket message: %v", err)
				return
			}
			var message wsMsg
			if err := json.Unmarshal(raw, &message); err != nil {
				t.Errorf("decode websocket message: %v", err)
				return
			}
			messages <- message
		}
	}))
	defer server.Close()

	url := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	writer := &wsWriter{conn: conn}
	if _, err := writer.Write([]byte("shell prompt")); err != nil {
		t.Fatalf("write terminal data: %v", err)
	}
	first := <-messages
	second := <-messages
	if first.Type != "ready" {
		t.Fatalf("first message type = %q, want ready", first.Type)
	}
	if second.Type != "data" || second.Data != "shell prompt" {
		t.Fatalf("second message = %#v, want terminal data", second)
	}
}

func TestTerminalOriginAllowsOnlyConfiguredUI(t *testing.T) {
	if !terminalOriginAllowed("https://app.breakfix.example", "https://app.breakfix.example") {
		t.Fatal("configured origin was rejected")
	}
	for _, origin := range []string{"https://evil.example", "http://app.breakfix.example", "https://app.breakfix.example/path", ""} {
		if terminalOriginAllowed(origin, "https://app.breakfix.example") {
			t.Fatalf("unexpected accepted origin %q", origin)
		}
	}
}

func TestTerminalTicketIsOpaqueAndStableHashed(t *testing.T) {
	ticket, err := newTerminalTicket()
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket) < 40 || terminalTicketHash(ticket) != terminalTicketHash(ticket) {
		t.Fatalf("invalid terminal ticket %q", ticket)
	}
}
