package session

import (
	"context"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

func TestControlPlaneSpawnIsPlacedWithoutAnExplicitNode(t *testing.T) {
	var got ports.SpawnConfig
	svc := &Service{fleetPlace: func(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
		got = cfg
		return domain.Session{SessionRecord: domain.SessionRecord{ID: "placed"}}, 0, 0, nil
	}}
	sess, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{Kind: domain.KindWorker, Harness: "codex", DisplayName: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "placed" || got.Harness != "codex" {
		t.Fatalf("session=%s harness=%s", sess.ID, got.Harness)
	}
}

func TestControlPlaneSendReachesThePlacement(t *testing.T) {
	var gotID, gotMessage string
	svc := &Service{fleetSend: func(_ context.Context, id, message string) (bool, error) {
		gotID, gotMessage = id, message
		return true, nil
	}}
	if err := svc.Send(context.Background(), "engine-1", "continue", nil); err != nil {
		t.Fatal(err)
	}
	if gotID != "engine-1" || gotMessage != "continue" {
		t.Fatalf("sent id=%s message=%s", gotID, gotMessage)
	}
}
