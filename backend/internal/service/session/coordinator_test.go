package session

import (
	"context"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

func TestStartCoordinatorRunsLocallyWithOpeningChoicesAndReplays(t *testing.T) {
	st := newFakeStore()
	st.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: t.TempDir()}
	fc := &fakeCommander{}
	fc.spawnFunc = func(cfg ports.SpawnConfig) domain.SessionRecord {
		rec := domain.SessionRecord{ID: "proj-coordinator", ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness, ClientRequestID: cfg.ClientRequestID, ClientRequestHash: cfg.ClientRequestHash, ClientRequestCommitted: true}
		st.sessions[rec.ID] = rec
		return rec
	}
	svc := NewWithDeps(Deps{Store: st, Manager: fc})
	fleetCalls := 0
	svc.SetFleetPlacement(func(context.Context, ports.SpawnConfig) (domain.Session, int, int, error) {
		fleetCalls++
		return domain.Session{}, 0, 0, nil
	})
	effort := "high"
	request := CoordinatorStartInput{ProjectID: "proj", Harness: domain.HarnessCodex, Model: "selected-model", Effort: &effort, Brief: "Inspect the project and plan the work", ClientRequestID: "opening-one", ClientRequestHash: "payload-hash"}
	first, err := svc.StartCoordinator(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.StartCoordinator(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || fc.spawnCalls != 1 || fleetCalls != 0 {
		t.Fatalf("first=%s second=%s local=%d remote=%d", first.ID, second.ID, fc.spawnCalls, fleetCalls)
	}
	cfg := fc.spawnedCfg
	if cfg.Kind != domain.KindOrchestrator || cfg.ExistingCheckout != st.projects["proj"].Path || cfg.AgentConfig.Model != "selected-model" || cfg.AgentConfig.Effort != "high" || cfg.Prompt != request.Brief {
		t.Fatalf("launch choices not preserved: %#v", cfg)
	}
	request.ClientRequestHash = "changed"
	if _, err := svc.StartCoordinator(t.Context(), request); err == nil {
		t.Fatal("mismatched retry accepted")
	}
}

func TestStartCoordinatorDoesNotReplaceOrDropMessageForExistingConversation(t *testing.T) {
	st := newFakeStore()
	st.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: t.TempDir()}
	st.sessions["existing"] = domain.SessionRecord{ID: "existing", ProjectID: "proj", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}
	fc := &fakeCommander{}
	svc := NewWithDeps(Deps{Store: st, Manager: fc})
	got, err := svc.StartCoordinator(t.Context(), CoordinatorStartInput{ProjectID: "proj", Harness: domain.HarnessPi})
	if err != nil || got.ID != "existing" || fc.spawnCalls != 0 {
		t.Fatalf("existing coordinator replaced: %#v %v", got, err)
	}
	_, err = svc.StartCoordinator(t.Context(), CoordinatorStartInput{ProjectID: "proj", Brief: "new work", ClientRequestID: "new-message", ClientRequestHash: "h"})
	if err == nil || fc.spawnCalls != 0 {
		t.Fatal("opening race silently discarded message or spawned another coordinator")
	}
}

func TestCoordinatorCannotBePlacedOnWorker(t *testing.T) {
	svc := &Service{fleetPlace: func(context.Context, ports.SpawnConfig) (domain.Session, int, int, error) {
		t.Fatal("coordinator reached fleet")
		return domain.Session{}, 0, 0, nil
	}}
	_, _, _, err := svc.Spawn(t.Context(), ports.SpawnConfig{Kind: domain.KindOrchestrator, ProjectID: "p", NodeID: "worker-a"})
	if err == nil {
		t.Fatal("remote coordinator placement accepted")
	}
}
