package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	reportsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/report"
	sessionsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/session"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type workerReportRPC struct {
	hub     *fleet.Hub
	handler fleet.ChatFunc
}

func (c workerReportRPC) Close() error { return nil }
func (c workerReportRPC) Send(ctx context.Context, command fleet.Command) error {
	var request struct {
		SessionID string          `json:"sessionId"`
		Op        string          `json:"op"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(command.Payload, &request); err != nil {
		return err
	}
	raw, err := c.handler(ctx, request.Op, request.SessionID, request.Payload)
	if err != nil {
		return err
	}
	c.hub.Publish(fleet.Event{CommandID: command.ID, Kind: fleet.EventChat, Payload: raw})
	return nil
}

func TestSQLiteWorkerReportReachesRemoteParentWithoutShadowSession(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	workerStore := sqlitetest.MustOpen(t)
	controlStore := sqlitetest.MustOpen(t)
	if err := workerStore.UpsertProject(ctx, domain.ProjectRecord{ID: "worker-project", Path: t.TempDir(), RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := controlStore.UpsertProject(ctx, domain.ProjectRecord{ID: "project", Path: t.TempDir(), RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	coordinator, err := controlStore.CreateSession(ctx, domain.SessionRecord{ProjectID: "project", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	const globalID = "fleet-worker-global"
	worker, err := workerStore.CreateSession(ctx, domain.SessionRecord{ProjectID: "worker-project", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, ClientRequestID: "fleet:" + globalID, ClientRequestHash: "launch-hash", Activity: domain.Activity{State: domain.ActivityActive}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := workerStore.CommitClientRequestSession(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	// The worker has no coordinator row. Its own local source id is the id
	// exported as AO_SESSION_ID by the existing engine and submitted by ao report.
	records, err := workerStore.ListSessions(ctx, "worker-project")
	if err != nil || len(records) != 1 || records[0].Kind != domain.KindWorker {
		t.Fatalf("worker sessions=%+v error=%v", records, err)
	}
	workerReports := reportsvc.New(reportsvc.Deps{Store: workerStore})
	created, err := workerReports.Create(ctx, reportsvc.CreateInput{SessionID: worker.ID, State: domain.ReportNeedsInput, Note: "Need coordinator review", Outputs: []domain.ReportOutput{{Kind: domain.ReportOutputArtifact, Reference: "artifact://result", Label: "Result"}}})
	if err != nil {
		t.Fatalf("actual worker report insertion failed: %v", err)
	}
	if created.SessionID != worker.ID || created.ProjectID != "worker-project" || created.DeliveryState != domain.ReportPending {
		t.Fatalf("worker outbox=%+v", created)
	}
	durable, found, err := workerStore.GetReport(ctx, created.ID)
	if err != nil || !found || len(durable.Outputs) != 1 {
		t.Fatalf("durable report=%+v found=%v error=%v", durable, found, err)
	}
	fleetStore := fleet.NewStore(controlStore.WriterDB())
	if err := fleetStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	placement := fleet.SessionPlacement{SessionID: globalID, NodeID: "worker-a", EngineID: string(worker.ID), ParentID: string(coordinator.ID), ProjectID: "project", Kind: "worker", Harness: "codex", Mode: "chat", State: fleet.PlacementRunning}
	if err := fleetStore.PutPlacement(ctx, placement); err != nil {
		t.Fatal(err)
	}
	hub := fleet.NewHub()
	hub.Attach("worker-a", workerReportRPC{hub: hub, handler: workerSupervision(nil, sessionsvc.NewWithDeps(sessionsvc.Deps{Store: workerStore}), workerStore)})
	service := fleet.NewService(fleetStore, hub)
	if err := service.PullReports(ctx); err != nil {
		t.Fatalf("real worker outbox transfer failed: %v", err)
	}
	inbox, err := fleetStore.PendingReportInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].SessionID != globalID || inbox[0].Report.ProjectID != "project" || len(inbox[0].Report.Outputs) != 1 {
		t.Fatalf("central receipt=%+v error=%v", inbox, err)
	}
	workerAck, found, err := workerStore.GetReport(ctx, created.ID)
	if err != nil || !found || workerAck.DeliveryState != domain.ReportAcknowledged {
		t.Fatalf("worker transfer ack=%+v found=%v error=%v", workerAck, found, err)
	}
	// A missing/busy coordinator leaves the real receipt durable for retry.
	busy := errors.New("coordinator busy")
	if err := service.DeliverReports(ctx, func(context.Context, fleet.SessionPlacement, domain.ReportRecord, string) error { return busy }); !errors.Is(err, busy) {
		t.Fatalf("delivery error=%v", err)
	}
	restarted := fleet.NewService(fleet.NewStore(controlStore.WriterDB()), hub)
	deliveries := 0
	err = restarted.DeliverReports(ctx, func(ctx context.Context, p fleet.SessionPlacement, report domain.ReportRecord, idempotencyKey string) error {
		parent, found, err := controlStore.GetSession(ctx, domain.SessionID(p.ParentID))
		if err != nil || !found || parent.ID != coordinator.ID || parent.Kind != domain.KindOrchestrator {
			t.Fatalf("remote parent=%+v found=%v error=%v", parent, found, err)
		}
		if report.SessionID != globalID || report.ProjectID != parent.ProjectID || idempotencyKey != inbox[0].ID {
			t.Fatalf("delivery routing=%+v key=%s", report, idempotencyKey)
		}
		deliveries++
		return nil
	})
	if err != nil || deliveries != 1 {
		t.Fatalf("delivery count=%d error=%v", deliveries, err)
	}
	pending, err := fleetStore.PendingReportInbox(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("accepted receipt still pending=%+v error=%v", pending, err)
	}
	// Transfer ack lives in the worker DB, so another poll cannot recreate a
	// receipt or require a local shadow of the central coordinator.
	if err := restarted.PullReports(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = fleetStore.PendingReportInbox(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("duplicate transfer=%+v error=%v", pending, err)
	}
	records, err = workerStore.ListSessions(ctx, "worker-project")
	if err != nil || len(records) != 1 || records[0].Kind != domain.KindWorker {
		t.Fatalf("shadow coordinator created: %+v error=%v", records, err)
	}
}
