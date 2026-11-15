package agy

import (
	"context"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	// Agy authenticates through its OS keyring and browser sign-in without a
	// documented non-interactive status command. Finding the binary proves only
	// installation, so authentication stays unknown until genuine evidence is
	// available. Launch remains authoritative; no credential state is invented.
	return ports.AgentAuthStatusUnknown, nil
}
