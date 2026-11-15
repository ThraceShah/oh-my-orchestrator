package sessionmanager

import (
	"context"
	"fmt"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// PrepareFleetSpawn resolves only authoritative project/parent launch choices.
// It deliberately does not discover binaries, models or provider credentials on
// this host: those belong to the worker chosen to run the session.
func (m *Manager) PrepareFleetSpawn(ctx context.Context, cfg ports.SpawnConfig, project domain.ProjectConfig) (ports.SpawnConfig, error) {
	if cfg.ParentSessionID != "" {
		parent, ok, err := m.store.GetSession(ctx, cfg.ParentSessionID)
		if err != nil {
			return cfg, fmt.Errorf("load requesting coordinator: %w", err)
		}
		if !ok || parent.ProjectID != cfg.ProjectID || parent.Kind != domain.KindOrchestrator || parent.IsTerminated {
			return cfg, fmt.Errorf("requesting coordinator is not active in this project")
		}
		if cfg.AgentConfig.Permissions == "" {
			permissions, err := m.inheritedSpawnPermissions(ctx, cfg.ProjectID, cfg.ParentSessionID)
			if err != nil {
				return cfg, err
			}
			cfg.AgentConfig.Permissions = permissions
		}
	}
	cfg.Harness = effectiveHarness(cfg.Harness, cfg.Kind, project)
	if cfg.Harness == "" {
		return cfg, ErrMissingHarness
	}
	cfg.AgentConfig = EffectiveSpawnAgentConfig(cfg, project)
	cfg.AgentConfigResolved = true
	cfg.RequestedMode = m.resolveSessionMode(ctx, cfg.Kind, cfg.RequestedMode)
	return cfg, nil
}
