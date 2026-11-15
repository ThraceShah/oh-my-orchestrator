package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
)

func TestWorkerCanCallControlWhileControlCallsSameWorker(t *testing.T) {
	store := openDB(t)
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	enrollment, err := store.Enroll(ctx, Node{ID: "worker", Name: "worker", Role: RoleWorker, Availability: AlwaysOn, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	listener := &Listener{Addr: "127.0.0.1:0", Store: store, Hub: hub}
	listener.SetControlHandler(func(ctx context.Context, event Event) json.RawMessage {
		if event.NodeID != "worker" {
			return EncodeSupervisionReply(nil, apierr.Forbidden("WRONG_OWNER", "Wrong node"))
		}
		// A synchronous listener handler would deadlock here waiting for a frame
		// that the same listener is no longer reading.
		reply, err := hub.Call(ctx, event.NodeID, Command{Kind: "read-local"})
		if err != nil {
			return EncodeSupervisionReply(nil, err)
		}
		return EncodeSupervisionReply(string(reply.Payload), nil)
	})
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx) }()
	for listener.Address() == "" {
		select {
		case <-ctx.Done():
			t.Fatal("listener not bound")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	dialer := &Dialer{URL: "ws://" + listener.Address() + "/fleet/v1/connect", NodeID: "worker", Token: enrollment.Token, Handler: func(_ context.Context, command Command) Event {
		return Event{Kind: "read", Payload: json.RawMessage(`"local-result"`)}
	}}
	dialer.StartNotifications()
	workerDone := make(chan error, 1)
	go func() { workerDone <- dialer.Run(ctx) }()
	for !hub.Connected("worker") {
		select {
		case <-ctx.Done():
			t.Fatal("worker not connected")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	var result string
	if err := dialer.CallControl(ctx, "session_send", map[string]string{"target": "coordinator"}, &result); err != nil {
		t.Fatal(err)
	}
	if result != `"local-result"` {
		t.Fatalf("result=%q", result)
	}
	cancel()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listener did not stop")
	}
}

func TestControlCallUnavailableDoesNotPretendAcceptance(t *testing.T) {
	var dialer Dialer
	err := dialer.CallControl(context.Background(), "session_send", nil, nil)
	var public *apierr.Error
	if !errors.As(err, &public) || public.Kind != apierr.KindUnavailable {
		t.Fatalf("error=%v", err)
	}
}

func TestReportSemanticDeliveryRetryKeepsReceiptAndParent(t *testing.T) {
	svc, placement := supervisionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	report := domain.ReportRecord{ID: "worker-report", SessionID: domain.SessionID(placement.EngineID), ProjectID: "worker-project", State: domain.ReportNeedsInput, Note: "Need review", CreatedAt: now, AvailableAt: now, DeliveryState: domain.ReportPending, RepeatCount: 1}
	if err := svc.store.acceptReport(ctx, placement, report); err != nil {
		t.Fatal(err)
	}
	transient := errors.New("coordinator busy")
	var key string
	if err := svc.DeliverReports(ctx, func(_ context.Context, p SessionPlacement, report domain.ReportRecord, id string) error {
		if p.ParentID != "coordinator" || report.SessionID != domain.SessionID(placement.SessionID) || report.ProjectID != "project" {
			t.Fatalf("routing=%+v report=%+v", p, report)
		}
		key = id
		return transient
	}); !errors.Is(err, transient) {
		t.Fatalf("error=%v", err)
	}
	pending, err := svc.store.PendingReportInbox(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("lost report=%v err=%v", pending, err)
	}
	restarted := NewService(NewStore(svc.store.db), svc.hub)
	deliveries := 0
	deliver := func(_ context.Context, _ SessionPlacement, _ domain.ReportRecord, id string) error {
		deliveries++
		if id != key {
			t.Fatal("delivery retry lost idempotency key")
		}
		return nil
	}
	if err := restarted.DeliverReports(ctx, deliver); err != nil {
		t.Fatal(err)
	}
	if err := restarted.DeliverReports(ctx, deliver); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries=%d", deliveries)
	}
}
