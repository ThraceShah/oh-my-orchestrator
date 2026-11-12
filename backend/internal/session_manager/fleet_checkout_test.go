package sessionmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

func TestExistingCheckoutIsUsedOnlyForAnOrchestrator(t *testing.T) {
	dir := t.TempDir()
	got, ok, err := existingCheckoutWorkspace(ports.SpawnConfig{
		Kind: domain.KindOrchestrator, ExistingCheckout: dir, ProjectID: "proj",
	}, "sess")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Path != dir || got.Branch != "HEAD" || got.SessionID != "sess" {
		t.Fatalf("workspace = %+v", got)
	}
	if _, ok, err := existingCheckoutWorkspace(ports.SpawnConfig{Kind: domain.KindWorker}, "sess"); ok || err != nil {
		t.Fatalf("worker without checkout ok=%v err=%v", ok, err)
	}
	_, ok, err = existingCheckoutWorkspace(ports.SpawnConfig{Kind: domain.KindWorker, ExistingCheckout: dir}, "sess")
	if !ok || err == nil {
		t.Fatal("a worker was allowed to adopt a fixed checkout")
	}
	missing := filepath.Join(dir, "missing")
	if _, ok, err = existingCheckoutWorkspace(ports.SpawnConfig{Kind: domain.KindOrchestrator, ExistingCheckout: missing}, "sess"); !ok || err == nil {
		t.Fatal("a missing checkout was accepted")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = existingCheckoutWorkspace(ports.SpawnConfig{Kind: domain.KindOrchestrator, ExistingCheckout: file}, "sess"); !ok || err == nil {
		t.Fatal("a file was accepted as a checkout")
	}
}
