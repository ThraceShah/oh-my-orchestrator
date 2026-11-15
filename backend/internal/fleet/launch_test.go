package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

func launchWorker(t *testing.T, store *Store, id string, availability Availability) {
	t.Helper()
	_, err := store.Enroll(context.Background(), Node{ID: id, Name: id, Role: RoleWorker, Availability: availability, Capacity: 2, PowerBrokerNode: "broker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Heartbeat(context.Background(), id, []Capability{{Harness: "pi", Installation: "installed", Authentication: "configured", Freshness: "stale", SupportedModes: []string{"chat", "tui"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestFleetStoppedPendingLaunchIsNotResurrectedOnReconnect(t *testing.T) {
	ctx := context.Background()
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	launchWorker(t, store, "worker", AlwaysOn)
	connection := &recordingConn{}
	hub.Attach("worker", connection)
	if _, err := svc.PlaceAndDispatch(ctx, Request{SessionID: "stopped-job", Kind: "worker", Harness: "pi"}, SpawnRequest{SessionID: "stopped-job", Kind: "worker", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlacement(ctx, SessionPlacement{SessionID: "stopped-job", NodeID: "worker", State: PlacementStopped}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResumeNode(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := svc.NoteStarted(ctx, "stopped-job", "worker", "late-engine", "chat"); err != nil {
		t.Fatal(err)
	}
	p, _, err := store.Placement(ctx, "stopped-job")
	if err != nil || p.State != PlacementStopped || len(connection.cmds) != 1 {
		t.Fatalf("placement=%+v commands=%d err=%v", p, len(connection.cmds), err)
	}
}

func TestFleetLaunchRefusesWorkersThatWouldDropParameters(t *testing.T) {
	ctx := context.Background()
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	launchWorker(t, store, "worker", AlwaysOn)
	connection := &recordingConn{}
	hub.Attach("worker", connection)
	config := ports.SpawnConfig{Harness: "pi", Kind: domain.KindWorker, AgentConfig: ports.AgentConfig{Model: "specific-model", Effort: "high"}}
	request := Request{SessionID: "versioned", Kind: "worker", Harness: "pi", NodeID: "worker"}
	spawn := SpawnRequest{SessionID: request.SessionID, Kind: "worker", Harness: "pi", Launch: &config}
	if _, err := svc.PlaceAndDispatch(ctx, request, spawn); err == nil {
		t.Fatal("legacy worker silently accepted complete launch")
	}
	if len(connection.cmds) != 0 {
		t.Fatal("legacy worker received a lossy launch")
	}
	if err := store.Heartbeat(ctx, "worker", []Capability{{Harness: "pi", Installation: "installed", Authentication: "configured", LaunchVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlaceAndDispatch(ctx, request, spawn); err != nil {
		t.Fatal(err)
	}
	if len(connection.cmds) != 1 {
		t.Fatalf("commands=%d", len(connection.cmds))
	}
	// A downgrade during a lost-ack reconnect must not silently replay the
	// richer request into an old worker either.
	if err := store.Heartbeat(ctx, "worker", []Capability{{Harness: "pi", LoggedIn: true}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResumeNode(ctx, "worker"); err == nil {
		t.Fatal("downgraded worker accepted pending full launch")
	}
	if len(connection.cmds) != 1 {
		t.Fatal("downgraded worker was sent a lossy replay")
	}
}

func TestFleetLaunchReplaysAcrossRestartAndRejectsHashMismatch(t *testing.T) {
	ctx := context.Background()
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	launchWorker(t, store, "worker", AlwaysOn)
	conn := &recordingConn{}
	hub.Attach("worker", conn)
	request := Request{SessionID: "request-1", Kind: "worker", Harness: "pi", Mode: "chat"}
	launch := SpawnRequest{SessionID: request.SessionID, Kind: "worker", Harness: "pi", Mode: "chat", ParentID: "coordinator", RequestHash: "hash-one"}
	first, err := svc.PlaceAndDispatch(ctx, request, launch)
	if err != nil {
		t.Fatal(err)
	}
	svc = NewService(store, hub)
	replay, err := svc.PlaceAndDispatch(ctx, request, launch)
	if err != nil {
		t.Fatal(err)
	}
	if first.NodeID != replay.NodeID || len(conn.cmds) != 1 {
		t.Fatalf("replay=%+v commands=%d", replay, len(conn.cmds))
	}
	if err := svc.ResumePending(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if len(conn.cmds) != 1 {
		t.Fatal("heartbeat resent an unacknowledged launch on the same connection")
	}
	launch.RequestHash = "hash-two"
	if _, err := svc.PlaceAndDispatch(ctx, request, launch); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("mismatch err=%v", err)
	}
	p, found, err := store.Placement(ctx, request.SessionID)
	if err != nil || !found || p.ParentID != "coordinator" || p.Mode != "chat" || len(p.Payload) == 0 {
		t.Fatalf("placement=%+v found=%v err=%v", p, found, err)
	}
	if err := svc.NoteStarted(ctx, request.SessionID, "worker", "engine-42"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResumeNode(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if len(conn.cmds) != 1 {
		t.Fatal("reconnect restarted an acknowledged engine")
	}
	p, _, _ = store.PlacementFor(ctx, "engine-42")
	if p.SessionID != request.SessionID || p.EngineID != "engine-42" || p.ParentID != "coordinator" || p.DispatchState != "acknowledged" {
		t.Fatalf("mapping=%+v", p)
	}
}

func TestFleetWakeContinuesPersistedLaunchExactlyOncePerConnection(t *testing.T) {
	ctx := context.Background()
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	launchWorker(t, store, "cold", OnDemand)
	// Enrollment stores power configuration, so use the fixture's loopback db
	// to configure it rather than invoking a shell or network power operation.
	_, err := store.db.ExecContext(ctx, `UPDATE fleet_nodes SET presence='offline',power_broker_node_id='broker',wake_command='deployment-configured-command' WHERE id='cold'`)
	if err != nil {
		t.Fatal(err)
	}
	broker := &recordingConn{}
	hub.Attach("broker", broker)
	req := Request{SessionID: "wake-job", Kind: "worker", Harness: "pi", NodeID: "cold", Mode: "chat"}
	spawn := SpawnRequest{SessionID: req.SessionID, Kind: "worker", Harness: "pi", Mode: "chat", Prompt: "resume this exact task", RequestHash: "wake-hash"}
	decision, err := svc.PlaceAndDispatch(ctx, req, spawn)
	if err != nil || decision.State != PlacementWaking || len(broker.cmds) != 1 {
		t.Fatalf("decision=%+v err=%v commands=%d", decision, err, len(broker.cmds))
	}
	// The control process can restart before the cold worker dials in.
	svc = NewService(store, hub)
	worker := &recordingConn{}
	hub.Attach("cold", worker)
	if err := svc.ResumeNode(ctx, "cold"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResumePending(ctx, "cold"); err != nil {
		t.Fatal(err)
	}
	if len(worker.cmds) != 1 || worker.cmds[0].Kind != CommandSpawn || !bytes.Contains(worker.cmds[0].Payload, []byte(spawn.Prompt)) {
		t.Fatalf("commands=%+v", worker.cmds)
	}
}

func TestFleetWorkerReceivesFullLaunchAndStableIdempotency(t *testing.T) {
	launch := ports.SpawnConfig{ProjectID: "project", ParentSessionID: "coordinator", Kind: domain.KindWorker, Harness: "pi", RequestedMode: domain.SessionModeChat, Prompt: "implement", AgentConfig: ports.AgentConfig{Model: "worker-model", Effort: "high", Permissions: "bypass-permissions"}, EffortOverride: true, AgentConfigResolved: true, Attachments: []ports.SpawnAttachment{{Ext: ".png", Data: []byte{1, 2, 3}}}, Async: true, NodeID: "worker", Platform: "linux", TaskPreparation: "controller-only", NameFromPrompt: true}
	exec := &recordingExec{}
	body, err := json.Marshal(SpawnRequest{SessionID: "global-job", Kind: "worker", Harness: "pi", Launch: &launch, RequestHash: "content-hash"})
	if err != nil {
		t.Fatal(err)
	}
	event := WorkerHandler(exec, nil, nil)(context.Background(), Command{Kind: CommandSpawn, Payload: body})
	if event.Kind != EventStarted {
		t.Fatalf("event=%+v", event)
	}
	expected := launch
	expected.NodeID, expected.Platform = "", ""
	expected.TaskPreparation = ""
	expected.ClientRequestID, expected.ClientRequestHash = "fleet:global-job", "content-hash"
	if !reflect.DeepEqual(exec.cfg, expected) {
		t.Fatalf("worker launch lost fields\ngot=%+v\nwant=%+v", exec.cfg, expected)
	}
}

func TestFleetCapabilitiesKeepActualProbeTimeAndAllowConfiguredCredentials(t *testing.T) {
	store := openDB(t)
	ctx := context.Background()
	launchWorker(t, store, "worker", AlwaysOn)
	checked := time.Now().Add(-time.Hour).UTC()
	capability := Capability{Harness: "pi", Installation: "installed", Authentication: "configured", Freshness: "stale", CheckedAt: &checked, ReportedAt: checked, SupportedModes: []string{"tui"}}
	if err := store.Heartbeat(ctx, "worker", []Capability{capability}); err != nil {
		t.Fatal(err)
	}
	node, _, err := store.Node(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	got := node.Capabilities[0]
	if !got.ReportedAt.Equal(checked) || got.CheckedAt == nil || !got.CheckedAt.Equal(checked) || got.Freshness != "stale" || got.LoggedIn {
		t.Fatalf("capability=%+v", got)
	}
	candidate := Candidate{Node: node}
	if _, err := Place(Request{Kind: "worker", Harness: "pi", Mode: "tui"}, []Candidate{candidate}); err != nil {
		t.Fatal(err)
	}
	if _, err := Place(Request{Kind: "worker", Harness: "pi", Mode: "chat"}, []Candidate{candidate}); !errors.Is(err, ErrNoNode) {
		t.Fatalf("unsupported chat mode err=%v", err)
	}
	raw, _ := json.Marshal(PublicNodes([]Node{{ID: "worker", WakeCommand: "secret command", ShutdownCommand: "secret command", ExpectedIdentity: "private identity", TokenHash: "private token", Capabilities: []Capability{got}}}))
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("private")) {
		t.Fatalf("unsafe public nodes: %s", raw)
	}
}
