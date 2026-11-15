package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	agentsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/agent"
)

type workerAgentCatalog interface {
	CachedReadiness(context.Context) (agentsvc.Readiness, error)
	EnsureReadiness(context.Context, []string, domain.AgentReadinessPurpose) (agentsvc.Readiness, error)
	Models(context.Context, string, string, bool) (ports.AgentModelCatalog, error)
}

type workerProjectRegistry interface {
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
}

// workerDiscovery uses the worker's own project/runtime/auth environment. Only
// normalized observations leave it; native credential/account diagnostics stay
// on that worker.
func workerDiscovery(agents workerAgentCatalog, registries ...workerProjectRegistry) fleet.QueryFunc {
	return func(ctx context.Context, query fleet.NodeQuery) (json.RawMessage, error) {
		if agents == nil {
			return nil, fmt.Errorf("worker agent catalog unavailable")
		}
		if query.ProjectID != "" {
			if len(registries) == 0 || registries[0] == nil {
				return nil, fmt.Errorf("worker project registry unavailable")
			}
			project, found, err := registries[0].GetProject(ctx, query.ProjectID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("project %q is not registered on this worker; register the same logical project ID on the worker before dispatch", query.ProjectID)
			}
			if project.Kind.WithDefault() != domain.ProjectKindScratch {
				info, err := os.Stat(project.Path)
				if err != nil || !info.IsDir() {
					return nil, fmt.Errorf("project %q checkout is unavailable on this worker", query.ProjectID)
				}
			}
		}
		switch query.Operation {
		case "models":
			if query.Agent == "" {
				return nil, fmt.Errorf("agent is required")
			}
			catalog, err := agents.Models(ctx, query.Agent, query.ProjectID, query.Refresh)
			if err != nil {
				return nil, err
			}
			return json.Marshal(catalog)
		case "readiness":
			var result agentsvc.Readiness
			var err error
			if query.Refresh {
				var ids []string
				if query.Agent != "" {
					ids = []string{query.Agent}
				}
				result, err = agents.EnsureReadiness(ctx, ids, domain.AgentReadinessPurpose("launch"))
			} else {
				result, err = agents.CachedReadiness(ctx)
			}
			if err != nil {
				return nil, err
			}
			clean := make([]domain.AgentReadinessSnapshot, 0, len(result.Agents))
			for _, snapshot := range result.Agents {
				if query.Agent != "" && query.Agent != snapshot.ID {
					continue
				}
				snapshot.Authentication.Account = nil
				snapshot.Authentication.Diagnostics = nil
				clean = append(clean, snapshot)
			}
			result.Agents = clean
			return json.Marshal(result)
		default:
			return nil, fmt.Errorf("unsupported worker query %q", query.Operation)
		}
	}
}
