package session

import (
	"context"
	"strings"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// CoordinatorStartInput describes the first human conversation for a project.
// Creation settings apply only to a new coordinator; an existing conversation
// is never replaced or silently reconfigured by an ensure request.
type CoordinatorStartInput struct {
	ProjectID         domain.ProjectID
	Clean             bool
	Harness           domain.AgentHarness
	Model             string
	Effort            *string
	Mode              domain.SessionMode
	Approval          domain.PermissionMode
	Brief             string
	Attachments       []ports.SpawnAttachment
	ClientRequestID   string
	ClientRequestHash string
}

// StartCoordinator ensures the local project conversation and its retry-safe opening message.
func (s *Service) StartCoordinator(ctx context.Context, in CoordinatorStartInput) (domain.Session, error) {
	if in.Harness != "" && !in.Harness.IsKnown() {
		return domain.Session{}, apierr.Invalid("UNKNOWN_HARNESS", "Unknown coordinator agent", nil)
	}
	if in.Clean && (in.ClientRequestID != "" || in.Brief != "") {
		return domain.Session{}, apierr.Invalid("COORDINATOR_REPLACEMENT_SEPARATE", "Open the existing coordinator before replacing it", nil)
	}
	if in.Brief != "" && in.ClientRequestID == "" {
		return domain.Session{}, apierr.Invalid("CLIENT_REQUEST_ID_REQUIRED", "An opening message needs a clientRequestId for safe retries", nil)
	}
	return s.spawnCoordinator(ctx, in.ProjectID, in.Clean, in.Mode, in.Approval, &in)
}

func (s *Service) coordinatorCheckout(project domain.ProjectRecord) (string, error) {
	if s.fleetPlace == nil {
		return "", nil
	}
	if strings.TrimSpace(project.Config.OrchestratorHomeNode) != "" {
		return "", apierr.Conflict("COORDINATOR_HOME_MIGRATION_REQUIRED", "The coordinator now runs on the service host. Remove the old worker home and register its local project checkout", nil)
	}
	checkout := strings.TrimSpace(project.Config.OrchestratorCheckout)
	if checkout == "" {
		checkout = project.Path
	}
	return checkout, nil
}

// prepareFleetSpawn resolves project and parent choices where the coordinator
// lives. It does not probe local binaries or models: the owning worker validates
// those in its own environment before launch.
func (s *Service) prepareFleetSpawn(ctx context.Context, cfg ports.SpawnConfig) (ports.SpawnConfig, error) {
	var project domain.ProjectRecord
	if cfg.ProjectID != "" {
		var err error
		project, err = s.requireProject(ctx, cfg.ProjectID)
		if err != nil {
			return cfg, err
		}
	}
	resolver, ok := s.manager.(interface {
		PrepareFleetSpawn(context.Context, ports.SpawnConfig, domain.ProjectConfig) (ports.SpawnConfig, error)
	})
	if ok {
		return resolver.PrepareFleetSpawn(ctx, cfg, project.Config)
	}
	// Focused service fakes need no runtime lookup, but role configuration is
	// still honored so scheduling never treats an omitted agent as unrestricted.
	if cfg.Harness == "" {
		cfg.Harness = project.Config.Worker.Harness
	}
	return cfg, nil
}
