package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
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

func TestWakeDispatchesTheConfiguredCommand(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "awake")
	if _, err := store.Enroll(ctx, Node{
		ID: "cold", Name: "cold", Role: RoleWorker, Availability: OnDemand, Capacity: 1,
		PowerBrokerNode: "broker", WakeCommand: "touch " + marker, ShutdownCommand: "true",
	}); err != nil {
		t.Fatal(err)
	}
	hub.Attach("broker", &recordingConn{})
	if _, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "job", Kind: "worker", NodeID: "cold"}, SpawnRequest{SessionID: "job", Kind: "worker"}); err != nil {
		t.Fatal(err)
	}
	conn := hubNode(t, hub, "broker")
	got, err := DecodePower(conn.cmds[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "wake" || got.Command != "touch "+marker || got.NodeID != "cold" {
		t.Fatalf("power = %+v", got)
	}
	if err := store.PutPlacement(ctx, SessionPlacement{SessionID: "job", NodeID: "cold", State: PlacementRunning}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPower(ctx, "cold", "shutdown"); err == nil {
		t.Fatal("shutdown was accepted while a session is running")
	}
}

func TestExecPowerRunsTheConfiguredCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "awake")
	if err := ExecPower(context.Background(), PowerRequest{NodeID: "cold", Action: "wake", Command: "touch " + marker}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	if err := ExecPower(context.Background(), PowerRequest{NodeID: "cold", Action: "wake"}); err == nil {
		t.Fatal("empty command was run")
	}
}

func TestBrokerDialInRunsTheWakeCommand(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "awake")
	enrolled, err := store.Enroll(ctx, Node{ID: "broker", Name: "broker", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1, PowerBroker: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enroll(ctx, Node{
		ID: "cold", Name: "cold", Role: RoleWorker, Availability: OnDemand, Capacity: 1,
		PowerBrokerNode: "broker", WakeCommand: "touch " + marker,
	}); err != nil {
		t.Fatal(err)
	}
	ln := &Listener{Addr: "127.0.0.1:0", Store: store, Hub: hub}
	go func() { _ = ln.Run(ctx) }()
	var addr string
	for i := 0; i < 50 && addr == ""; i++ {
		addr = ln.Address()
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("listener did not bind")
	}
	go func() {
		_ = (&Dialer{
			URL: "ws://" + addr + "/fleet/v1/connect", NodeID: "broker", Token: enrolled.Token,
			Handler: WorkerHandler(nil, ExecPower, nil),
		}).Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !hub.Connected("broker") {
		if time.Now().After(deadline) {
			t.Fatal("broker did not dial in")
		}
		time.Sleep(20 * time.Millisecond)
	}
	svc := NewService(store, hub)
	if _, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "job", Kind: "worker", NodeID: "cold"}, SpawnRequest{SessionID: "job"}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("wake command did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hubNode(t *testing.T, hub *Hub, id string) *recordingConn {
	t.Helper()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	conn, ok := hub.nodes[id].conn.(*recordingConn)
	if !ok {
		t.Fatalf("node %s is not the recording connection", id)
	}
	return conn
}

func TestBoardSessionsUsesTheEngineIDAndSurvivesStatusUpdates(t *testing.T) {
	store := openDB(t)
	svc := NewService(store, NewHub())
	ctx := context.Background()
	if _, err := store.Enroll(ctx, Node{ID: "w", Name: "w", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	_ = store.Heartbeat(ctx, "w", []Capability{{Harness: "claude-code", LoggedIn: true}})
	hub := NewHub()
	svc = NewService(store, hub)
	hub.Attach("w", &recordingConn{})
	if _, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "job", Kind: "worker", Harness: "claude-code"}, SpawnRequest{SessionID: "job", Kind: "worker", Harness: "claude-code", DisplayName: "Remote"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.NoteStarted(ctx, "job", "w", "engine-1"); err != nil {
		t.Fatal(err)
	}
	board, err := svc.BoardSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 1 || string(board[0].ID) != "engine-1" || board[0].OwnerNodeID != "w" || board[0].DisplayName != "Remote" || board[0].PlacementState != string(PlacementRunning) {
		t.Fatalf("board = %+v", board)
	}
}

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
