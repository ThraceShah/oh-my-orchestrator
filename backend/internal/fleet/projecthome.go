package fleet

import (
	"context"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	projectsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/project"
)

// ProjectConfigHomes reads orchestrator home settings from project config.
type ProjectConfigHomes struct {
	Projects interface {
		Get(context.Context, domain.ProjectID) (projectsvc.GetResult, error)
	}
}

func (h ProjectConfigHomes) Home(ctx context.Context, id domain.ProjectID) (ProjectHome, error) {
	if h.Projects == nil {
		return ProjectHome{}, ErrHomeRequired
	}
	got, err := h.Projects.Get(ctx, id)
	if err != nil {
		return ProjectHome{}, err
	}
	if got.Project == nil || got.Project.Config == nil {
		return ProjectHome{}, nil
	}
	return ProjectHome{NodeID: got.Project.Config.OrchestratorHomeNode, Checkout: got.Project.Config.OrchestratorCheckout}, nil
}
