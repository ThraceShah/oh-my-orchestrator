package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

type crossMessageSessions struct {
	project domain.ProjectID
	sent    int
	options ports.MessageDeliveryOptions
	target  domain.SessionID
}

func (f *crossMessageSessions) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	return domain.Session{SessionRecord: domain.SessionRecord{ID: id, ProjectID: f.project, Kind: domain.KindOrchestrator}}, nil
}
func (f *crossMessageSessions) SendWithOptions(_ context.Context, id domain.SessionID, _ string, _ *ports.SpawnAttachment, options ports.MessageDeliveryOptions) error {
	f.sent++
	f.target = id
	f.options = options
	return nil
}

func TestCrossNodeMessagesUseAuthenticatedOwnerAndGlobalSender(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := fleet.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPlacement(ctx, fleet.SessionPlacement{SessionID: "global-worker", EngineID: "engine-local", NodeID: "worker-a", ProjectID: "project", Kind: "worker", ParentID: "coordinator", State: fleet.PlacementRunning}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, node, project string
		wantForbidden       bool
	}{{"valid", "worker-a", "project", false}, {"wrong node", "worker-b", "project", true}, {"other project", "worker-a", "other", true}} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &crossMessageSessions{project: domain.ProjectID(tc.project)}
			message, _ := json.Marshal(crossNodeMessage{Target: "coordinator", SessionMessage: fleet.SessionMessage{Message: "Need review", Options: ports.MessageDeliveryOptions{SenderSessionID: "engine-local", AuthoredByUser: true}}})
			request, _ := json.Marshal(fleet.ControlRequest{Op: "session_send", Payload: message})
			raw := controlWorkerRequests(&fleet.Runtime{Store: store}, sessions)(ctx, fleet.Event{NodeID: tc.node, Payload: request})
			err := fleet.DecodeSupervisionReply(raw, nil)
			if tc.wantForbidden {
				var public *apierr.Error
				if !errors.As(err, &public) || public.Kind != apierr.KindForbidden || sessions.sent != 0 {
					t.Fatalf("error=%v sends=%d", err, sessions.sent)
				}
				return
			}
			if err != nil || sessions.sent != 1 || sessions.target != "coordinator" || sessions.options.SenderSessionID != "global-worker" || sessions.options.AuthoredByUser {
				t.Fatalf("error=%v sessions=%+v", err, sessions)
			}
		})
	}
}
