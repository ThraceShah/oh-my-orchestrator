package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestEnrollHeartbeatAndExpiryDoNotImplyDeath(t *testing.T) {
	store := openDB(t)
	ctx := context.Background()
	enrolled, err := store.Enroll(ctx, Node{ID: "n1", Name: "worker-1", Role: RoleWorker, Availability: AlwaysOn, Capacity: 2, Platforms: []string{"linux"}})
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.Token == "" {
		t.Fatal("token was not issued")
	}
	if _, err := store.Authenticate(ctx, "n1", "nope", ""); err == nil {
		t.Fatal("bad token was accepted")
	}
	if _, err := store.Authenticate(ctx, "n1", enrolled.Token, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Heartbeat(ctx, "n1", []Capability{{Harness: "claude-code", LoggedIn: true, Version: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlacement(ctx, SessionPlacement{SessionID: "s1", NodeID: "n1", State: PlacementRunning}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Now().Add(time.Hour) }
	expired, err := store.Expire(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != "n1" {
		t.Fatalf("expired = %v", expired)
	}
	placement, ok, err := store.Placement(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("placement ok=%v err=%v", ok, err)
	}
	if placement.State != PlacementOffline {
		t.Fatalf("state = %s, want offline without termination", placement.State)
	}
}

func TestStartedEventRecordsTheWorkerEngineID(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go applyEvents(ctx, hub, svc, nil)
	if _, err := store.Enroll(ctx, Node{ID: "w", Name: "w", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	_ = store.Heartbeat(ctx, "w", nil)
	fake := &recordingConn{}
	hub.Attach("w", fake)
	if _, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "placement-1", Kind: "worker", NodeID: "w"}, SpawnRequest{SessionID: "placement-1", Kind: "worker"}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"sessionId": "engine-9"})
	hub.Publish(Event{NodeID: "w", SessionID: "placement-1", Kind: EventStarted, Payload: body})
	deadline := time.Now().Add(2 * time.Second)
	var got SessionPlacement
	for {
		p, ok, err := store.Placement(ctx, "placement-1")
		if err != nil {
			t.Fatal(err)
		}
		if ok && p.State == PlacementRunning {
			got = p
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("placement did not become running: %+v", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.EngineID != "engine-9" || got.NodeID != "w" {
		t.Fatalf("placement = %+v", got)
	}
}

func TestServiceDispatchesToTheConnectedWorker(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	ctx := context.Background()
	if _, err := store.Enroll(ctx, Node{ID: "w", Name: "w", Role: RoleWorker, Availability: AlwaysOn, Capacity: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.Heartbeat(ctx, "w", []Capability{{Harness: "claude-code", LoggedIn: true}}); err != nil {
		t.Fatal(err)
	}
	fake := &recordingConn{}
	hub.Attach("w", fake)
	decision, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "s", Kind: "worker", Harness: "claude-code"}, SpawnRequest{SessionID: "s", Kind: "worker", Harness: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.NodeID != "w" || decision.State != PlacementStarting {
		t.Fatalf("decision = %+v", decision)
	}
	if len(fake.cmds) != 1 || fake.cmds[0].Kind != CommandSpawn {
		t.Fatalf("commands = %+v", fake.cmds)
	}
}

func TestServiceDoesNotRetargetWhenTheWorkerIsGone(t *testing.T) {
	store := openDB(t)
	svc := NewService(store, NewHub())
	ctx := context.Background()
	if _, err := store.Enroll(ctx, Node{ID: "w", Name: "w", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	_ = store.Heartbeat(ctx, "w", nil)
	if _, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "s", Kind: "worker", NodeID: "w"}, SpawnRequest{SessionID: "s"}); err == nil {
		t.Fatal("dispatch succeeded without a connection")
	}
	placement, ok, err := store.Placement(ctx, "s")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if placement.State != PlacementFailed || placement.NodeID != "w" {
		t.Fatalf("placement = %+v", placement)
	}
}

func TestListenerAcceptsAndRejects(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	ctx := context.Background()
	enrolled, err := store.Enroll(ctx, Node{
		ID: "w", Name: "w", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1,
		ExpectedIdentity: "node-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = hub
	if _, err := store.Authenticate(ctx, "w", enrolled.Token, "node-b"); err == nil {
		t.Fatal("mismatched identity was accepted")
	}
	if _, err := store.Authenticate(ctx, "w", enrolled.Token, "node-a"); err != nil {
		t.Fatal(err)
	}
}

type recordingConn struct {
	cmds []Command
}

func (c *recordingConn) Send(_ context.Context, cmd Command) error {
	c.cmds = append(c.cmds, cmd)
	return nil
}

func (c *recordingConn) Close() error { return nil }

func TestLocalCapabilitiesKeepsCredentialsOffTheWire(t *testing.T) {
	caps, err := LocalCapabilities(context.Background(), func(context.Context) ([]string, []string, error) {
		return []string{"claude-code", "codex"}, []string{"claude-code"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 2 || !caps[0].LoggedIn || caps[1].LoggedIn || caps[1].Harness != "codex" {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestDialerRejectsIncompleteConfig(t *testing.T) {
	err := (&Dialer{}).Run(context.Background())
	if err == nil {
		t.Fatal(errors.New("expected an error"))
	}
}
