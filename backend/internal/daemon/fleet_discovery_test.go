package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	agentsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/agent"
)

type discoveryCatalog struct {
	modelsCalled bool
	snapshot     domain.AgentReadinessSnapshot
}

func (d *discoveryCatalog) List(context.Context) (agentsvc.Inventory, error) {
	return agentsvc.Inventory{}, errors.New("legacy inventory must not replace normalized readiness")
}
func (d *discoveryCatalog) CachedReadiness(context.Context) (agentsvc.Readiness, error) {
	return agentsvc.Readiness{Agents: []domain.AgentReadinessSnapshot{d.snapshot}}, nil
}
func (d *discoveryCatalog) EnsureReadiness(context.Context, []string, domain.AgentReadinessPurpose) (agentsvc.Readiness, error) {
	return d.CachedReadiness(context.Background())
}
func (d *discoveryCatalog) Models(context.Context, string, string, bool) (ports.AgentModelCatalog, error) {
	d.modelsCalled = true
	return ports.AgentModelCatalog{}, nil
}

type discoveryProjects struct {
	project domain.ProjectRecord
	found   bool
}

func (d discoveryProjects) GetProject(context.Context, string) (domain.ProjectRecord, bool, error) {
	return d.project, d.found, nil
}

func TestWorkerDiscoveryRequiresRealProjectBindingBeforeModelLookup(t *testing.T) {
	for _, operation := range []string{"models", "readiness"} {
		t.Run(operation, func(t *testing.T) {
			catalog := &discoveryCatalog{}
			query := workerDiscovery(catalog, discoveryProjects{})
			_, err := query(context.Background(), fleet.NodeQuery{Operation: operation, Agent: "pi", ProjectID: "controller-project"})
			if err == nil || !strings.Contains(err.Error(), "not registered on this worker") || catalog.modelsCalled {
				t.Fatalf("err=%v modelLookup=%v", err, catalog.modelsCalled)
			}
		})
	}
	catalog := &discoveryCatalog{}
	query := workerDiscovery(catalog, discoveryProjects{found: true, project: domain.ProjectRecord{Path: t.TempDir()}})
	if _, err := query(context.Background(), fleet.NodeQuery{Operation: "models", Agent: "pi", ProjectID: "registered"}); err != nil || !catalog.modelsCalled {
		t.Fatalf("err=%v lookup=%v", err, catalog.modelsCalled)
	}
	query = workerDiscovery(catalog, discoveryProjects{found: true, project: domain.ProjectRecord{Path: "/path/that/does/not/exist"}})
	if _, err := query(context.Background(), fleet.NodeQuery{Operation: "models", Agent: "pi", ProjectID: "registered"}); err == nil || !strings.Contains(err.Error(), "checkout is unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkerDiscoveryKeepsProviderAccountPrivateAndProbeTimeTruthful(t *testing.T) {
	checked := time.Now().Add(-time.Hour)
	catalog := &discoveryCatalog{snapshot: domain.AgentReadinessSnapshot{ID: "pi", Installation: domain.AgentInstallationObservation{State: "installed", Freshness: "fresh", CheckedAt: &checked}, Authentication: domain.AgentAuthenticationObservation{State: "configured", Freshness: "stale", CheckedAt: &checked, Account: &domain.AgentAccountIdentity{Email: "private@example.invalid", Source: "private-config-path"}}}}
	raw, err := workerDiscovery(catalog)(context.Background(), fleet.NodeQuery{Operation: "readiness", Agent: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private")) {
		t.Fatalf("account leaked: %s", raw)
	}
	caps := workerCapabilities(context.Background(), catalog, func(h domain.AgentHarness) bool { return h == "pi" })
	if len(caps) != 1 || caps[0].Authentication != "configured" || caps[0].LoggedIn || caps[0].Freshness != "stale" || caps[0].CheckedAt == nil || !caps[0].CheckedAt.Equal(checked) || len(caps[0].SupportedModes) != 2 {
		t.Fatalf("caps=%+v", caps)
	}
}
