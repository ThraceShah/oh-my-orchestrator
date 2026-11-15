package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	sessionmanager "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/session_manager"
)

type supervisionConn struct {
	hub  *Hub
	call func(string, string, json.RawMessage) (any, error)
}

func (c supervisionConn) Close() error { return nil }
func (c supervisionConn) Send(_ context.Context, cmd Command) error {
	var request struct {
		SessionID string          `json:"sessionId"`
		Op        string          `json:"op"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(cmd.Payload, &request); err != nil {
		return err
	}
	value, err := c.call(request.Op, request.SessionID, request.Payload)
	c.hub.Publish(Event{CommandID: cmd.ID, Kind: EventChat, Payload: EncodeSupervisionReply(value, err)})
	return nil
}

func supervisionFixture(t *testing.T) (*Service, SessionPlacement) {
	t.Helper()
	store := openDB(t)
	hub := NewHub()
	svc := NewService(store, hub)
	p := SessionPlacement{SessionID: "global-job", EngineID: "engine-1", NodeID: "worker-a", ProjectID: "project", Kind: "worker", Harness: "codex", Mode: "chat", ParentID: "coordinator", State: PlacementRunning}
	if err := store.PutPlacement(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return svc, p
}

func TestRemoteReadPreservesRealFactsAndGlobalIdentity(t *testing.T) {
	svc, p := supervisionFixture(t)
	svc.hub.Attach(p.NodeID, supervisionConn{hub: svc.hub, call: func(op, id string, _ json.RawMessage) (any, error) {
		if op != "session_get" || id != p.EngineID {
			t.Fatalf("RPC %s %s", op, id)
		}
		return domain.Session{SessionRecord: domain.SessionRecord{ID: domain.SessionID(id), Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityActive}}, Status: domain.StatusWorking, KanbanColumn: domain.KanbanNeedsReview, StatusReadiness: "ready"}, nil
	}})
	got, handled, err := svc.GetSession(context.Background(), domain.SessionID(p.SessionID))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if got.ID != domain.SessionID(p.SessionID) || got.ParentSessionID != "coordinator" || got.Mode != domain.SessionModeChat || got.Status != domain.StatusWorking || got.KanbanColumn != domain.KanbanNeedsReview || got.TerminalHandleID != "" {
		t.Fatalf("session=%+v", got)
	}
	board, err := svc.BoardSessions(context.Background())
	if err != nil || len(board) != 1 || board[0].Status != domain.StatusWorking {
		t.Fatalf("board=%+v err=%v", board, err)
	}
}

func TestOfflineObservationDoesNotInventIdleOrTermination(t *testing.T) {
	svc, p := supervisionFixture(t)
	got, err := svc.BoardSessions(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("board=%v err=%v", got, err)
	}
	s := got[0]
	if s.ID != domain.SessionID(p.SessionID) || s.Status != domain.StatusNoSignal || s.StatusReadiness != "unavailable" || s.KanbanColumn != "" || s.IsTerminated || s.Activity.State != "" || s.Mode != domain.SessionModeChat {
		t.Fatalf("invented observation: %+v", s)
	}
	_, handled, err := svc.GetSession(context.Background(), s.ID)
	var public *apierr.Error
	if !handled || !errors.As(err, &public) || public.Kind != apierr.KindUnavailable {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestRemoteLifecycleAndMessageKeepOwnerAndChoices(t *testing.T) {
	svc, p := supervisionFixture(t)
	calls := 0
	svc.hub.Attach(p.NodeID, supervisionConn{hub: svc.hub, call: func(op, id string, payload json.RawMessage) (any, error) {
		calls++
		if id != p.EngineID {
			t.Fatalf("id=%s", id)
		}
		switch op {
		case "session_send":
			var body SessionMessage
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatal(err)
			}
			if body.Attachment == nil || body.Message != "hello" || body.Options.SenderSessionID != "coordinator" {
				t.Fatalf("body=%+v", body)
			}
			return nil, nil
		case "session_request_kill":
			return sessionmanager.KillResult{CleanupPending: true}, nil
		case "session_kill":
			return true, nil
		default:
			t.Fatalf("op=%s", op)
			return nil, nil
		}
	}})
	handled, err := svc.SendSession(context.Background(), domain.SessionID(p.SessionID), "hello", &ports.SpawnAttachment{Ext: ".png", Data: []byte{1}}, ports.MessageDeliveryOptions{SenderSessionID: "coordinator"})
	if !handled || err != nil {
		t.Fatalf("send=%v %v", handled, err)
	}
	result, handled, err := svc.RequestKillSession(context.Background(), domain.SessionID(p.SessionID))
	if !handled || err != nil || !result.CleanupPending {
		t.Fatalf("kill=%+v %v %v", result, handled, err)
	}
	freed, handled, err := svc.KillSession(context.Background(), domain.SessionID(p.SessionID))
	if !handled || err != nil || !freed {
		t.Fatalf("kill=%v %v %v", freed, handled, err)
	}
	placement, _, _ := svc.store.Placement(context.Background(), p.SessionID)
	if placement.State != PlacementStopped || calls != 3 {
		t.Fatalf("placement=%+v calls=%d", placement, calls)
	}
	fallback := unobservedSession(placement)
	if !fallback.IsTerminated || fallback.Status != domain.StatusTerminated {
		t.Fatalf("lost acknowledged terminal intent: %+v", fallback)
	}
	active, err := svc.store.CountActive(context.Background(), p.NodeID)
	if err != nil || active != 0 {
		t.Fatalf("released capacity=%d err=%v", active, err)
	}
}

func TestFailedPlacementNeverClaimsItIsStillStarting(t *testing.T) {
	for _, connected := range []bool{false, true} {
		name := "offline"
		if connected {
			name = "connected"
		}
		t.Run(name, func(t *testing.T) {
			svc, placement := supervisionFixture(t)
			placement.EngineID = ""
			placement.State = PlacementFailed
			placement.Detail = `{"message":"The selected model is unavailable on this worker"}`
			placement.SessionID = "failed-before-engine"
			if err := svc.store.PutPlacement(context.Background(), placement); err != nil {
				t.Fatal(err)
			}
			if connected {
				svc.hub.Attach(placement.NodeID, supervisionConn{hub: svc.hub, call: func(string, string, json.RawMessage) (any, error) {
					t.Fatal("failed launch issued worker RPC")
					return nil, nil
				}})
			}
			_, handled, err := svc.GetSession(context.Background(), domain.SessionID(placement.SessionID))
			var public *apierr.Error
			if !handled || !errors.As(err, &public) || public.Kind != apierr.KindUnavailable || public.Code != "FLEET_SESSION_FAILED" {
				t.Fatalf("handled=%v error=%v", handled, err)
			}
			if public.Details["reason"] != "The selected model is unavailable on this worker" {
				t.Fatalf("lost failure reason: %+v", public)
			}
			fallback := unobservedSession(placement)
			if fallback.ProvisionState != domain.SessionProvisionFailed || fallback.ProvisionError != public.Details["reason"] {
				t.Fatalf("board/GET failure disagree: %+v", fallback)
			}
		})
	}
}

func TestWorkerEngineIdentityCannotShadowLocalCoordinator(t *testing.T) {
	svc, p := supervisionFixture(t)
	svc.hub.Attach(p.NodeID, supervisionConn{hub: svc.hub, call: func(string, string, json.RawMessage) (any, error) {
		t.Fatal("engine alias was routed remotely")
		return nil, nil
	}})
	_, handled, err := svc.GetSession(context.Background(), domain.SessionID(p.EngineID))
	if handled || err != nil {
		t.Fatalf("engine get handled=%v err=%v", handled, err)
	}
	_, handled, err = svc.Chat(context.Background(), p.EngineID, "snapshot", nil)
	if handled || err != nil {
		t.Fatalf("engine chat handled=%v err=%v", handled, err)
	}
}

func TestRemoteErrorsPreservePublicKind(t *testing.T) {
	svc, p := supervisionFixture(t)
	svc.hub.Attach(p.NodeID, supervisionConn{hub: svc.hub, call: func(string, string, json.RawMessage) (any, error) {
		return nil, apierr.Conflict("DIRTY_WORKTREE", "The workspace has uncommitted changes", nil)
	}})
	_, handled, err := svc.KillSession(context.Background(), domain.SessionID(p.SessionID))
	var public *apierr.Error
	if !handled || !errors.As(err, &public) || public.Kind != apierr.KindConflict || public.Code != "DIRTY_WORKTREE" {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	placement, _, _ := svc.store.Placement(context.Background(), p.SessionID)
	if placement.State != PlacementRunning {
		t.Fatal("failed kill changed lifecycle")
	}
}

func TestReportTransferIsDurableAndDeduplicatedAcrossNodes(t *testing.T) {
	svc, p := supervisionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	report := domain.ReportRecord{ID: "report-1", SessionID: domain.SessionID(p.EngineID), ProjectID: "worker-project", State: domain.ReportNeedsInput, Note: "Need a decision", CreatedAt: now, AvailableAt: now, DeliveryState: domain.ReportPending, RepeatCount: 1}
	acks := 0
	svc.hub.Attach(p.NodeID, supervisionConn{hub: svc.hub, call: func(op, id string, payload json.RawMessage) (any, error) {
		switch op {
		case "session_reports":
			return []domain.ReportRecord{report}, nil
		case "session_report_ack":
			acks++
			pending, err := svc.store.PendingReportInbox(ctx)
			if err != nil || (acks < 3 && len(pending) != 1) {
				t.Fatalf("worker ack before durable receipt: %v %v", pending, err)
			}
			return nil, nil
		default:
			t.Fatalf("op=%s", op)
			return nil, nil
		}
	}})
	if err := svc.PullReports(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.PullReports(ctx); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(svc.store.db)
	pending, err := restarted.PendingReportInbox(ctx)
	if err != nil || len(pending) != 1 || acks != 2 || pending[0].SessionID != "global-job" || pending[0].Report.ProjectID != "project" {
		t.Fatalf("pending=%+v acks=%d err=%v", pending, acks, err)
	}
	if err := restarted.AcknowledgeReportInbox(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.PullReports(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = restarted.PendingReportInbox(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("duplicate resurrected receipt: %v %v", pending, err)
	}
}
