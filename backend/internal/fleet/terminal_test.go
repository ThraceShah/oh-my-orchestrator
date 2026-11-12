package fleet

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTerminalRelayReachesTheOwningWorker(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	enrolled, err := store.Enroll(ctx, Node{ID: "w", Name: "w", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlacement(ctx, SessionPlacement{SessionID: "job", NodeID: "w", State: PlacementRunning, EngineID: "engine-1"}); err != nil {
		t.Fatal(err)
	}
	ln := &Listener{Addr: "127.0.0.1:0", Store: store, Hub: hub}
	go func() { _ = ln.Run(ctx) }()
	addr := waitAddr(t, ln)
	go func() {
		_ = (&Dialer{
			URL: "ws://" + addr + "/fleet/v1/connect", NodeID: "w", Token: enrolled.Token,
			OnTerminal: (&Bridge{Open: func(context.Context, string, uint16, uint16) (TerminalStream, error) {
				return &echoStream{Reader: bytes.NewReader([]byte("hello"))}, nil
			}}).Handle,
		}).Run(ctx)
	}()
	waitConnected(t, hub, "w")
	srv := httptest.NewServer(&Relay{Hub: hub, Store: store})
	defer srv.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/ignored", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := writeClient(ctx, conn, terminalClientMessage{Type: "open", SessionID: "engine-1"}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(got, []byte("hello")) {
		if time.Now().After(deadline) {
			t.Fatalf("relayed %q", got)
		}
		var msg terminalClientMessage
		if err := readClient(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "error" {
			t.Fatal(msg.Error)
		}
		if msg.Type == "data" {
			raw, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, raw...)
		}
	}
}

type echoStream struct{ io.Reader }

func (e *echoStream) Write(p []byte) (int, error) { return len(p), nil }
func (e *echoStream) Close() error                { return nil }

func waitAddr(t *testing.T, ln *Listener) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := ln.Address(); addr != "" {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener did not bind")
	return ""
}

func waitConnected(t *testing.T, hub *Hub, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Connected(id) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not connect", id)
}
