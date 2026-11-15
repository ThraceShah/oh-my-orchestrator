package session

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	sessionmanager "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/session_manager"
)

type fakeFleetSupervisor struct {
	err     error
	sent    int
	options ports.MessageDeliveryOptions
}

func (f *fakeFleetSupervisor) GetSession(context.Context, domain.SessionID) (domain.Session, bool, error) {
	return domain.Session{SessionRecord: domain.SessionRecord{ID: "global", Mode: domain.SessionModeChat}}, true, f.err
}
func (f *fakeFleetSupervisor) KillSession(context.Context, domain.SessionID) (bool, bool, error) {
	return true, true, f.err
}
func (f *fakeFleetSupervisor) RequestKillSession(context.Context, domain.SessionID) (sessionmanager.KillResult, bool, error) {
	return sessionmanager.KillResult{CleanupPending: true}, true, f.err
}
func (f *fakeFleetSupervisor) SendSession(_ context.Context, _ domain.SessionID, _ string, _ *ports.SpawnAttachment, options ports.MessageDeliveryOptions) (bool, error) {
	f.sent++
	f.options = options
	return true, f.err
}

func TestRemoteSupervisionNeverFallsThroughToLocalManager(t *testing.T) {
	ctx := context.Background()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "offline"}[failed], func(t *testing.T) {
			remote := &fakeFleetSupervisor{}
			if failed {
				remote.err = apierr.Unavailable("FLEET_NODE_OFFLINE", "Worker offline")
			}
			// A nil local manager is intentional: every handled operation must return
			// from the remote boundary even when the worker fails.
			svc := &Service{}
			svc.SetFleetSupervision(remote)
			session, err := svc.Get(ctx, "global")
			if !errors.Is(err, remote.err) || (!failed && session.Mode != domain.SessionModeChat) {
				t.Fatalf("get=%+v err=%v", session, err)
			}
			_, err = svc.Kill(ctx, "global")
			if !errors.Is(err, remote.err) {
				t.Fatal(err)
			}
			result, err := svc.RequestKill(ctx, "global")
			if !errors.Is(err, remote.err) || (!failed && !result.CleanupPending) {
				t.Fatalf("request kill=%+v %v", result, err)
			}
			if err := svc.Send(ctx, "global", "hello", &ports.SpawnAttachment{Ext: ".png"}); !errors.Is(err, remote.err) {
				t.Fatal(err)
			}
			if err := svc.SendWithOptions(ctx, "global", "hello", nil, ports.MessageDeliveryOptions{SenderSessionID: "coordinator"}); !errors.Is(err, remote.err) {
				t.Fatal(err)
			}
			if remote.sent != 2 || remote.options.SenderSessionID != "coordinator" {
				t.Fatalf("remote=%+v", remote)
			}
		})
	}
}
