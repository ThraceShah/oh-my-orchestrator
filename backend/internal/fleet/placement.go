package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// ProjectHome is the orchestrator checkout configured for one project. Both
// fields empty means the project has no fleet home, so an orchestrator session
// cannot be placed.
type ProjectHome struct {
	NodeID   string
	Checkout string
}

// ProjectHomes reads a project's orchestrator home. The control plane supplies
// it from project config; tests supply a fixed value.
type ProjectHomes interface {
	Home(context.Context, domain.ProjectID) (ProjectHome, error)
}

// PlacementService turns a spawn that names a fleet target into a recorded
// placement and a command to the owning worker. It returns a session read
// model whose activity is idle because the worker has not reported one yet.
// The placement state is the durable fact; it is not a display status.
type PlacementService struct {
	Fleet    *Service
	Projects ProjectHomes
}

// Place records worker ownership and dispatches a complete session launch.
func (p *PlacementService) Place(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	if p == nil || p.Fleet == nil {
		return domain.Session{}, 0, 0, apierr.Invalid("FLEET_NOT_CONFIGURED", "This daemon is not a fleet control plane", nil)
	}
	req := Request{
		Kind:     string(cfg.Kind),
		Harness:  string(cfg.Harness),
		Platform: cfg.Platform,
		NodeID:   cfg.NodeID,
		Mode:     string(cfg.RequestedMode),
	}
	if cfg.Kind == "" {
		req.Kind = string(domain.KindWorker)
	}
	if req.Kind == string(domain.KindOrchestrator) {
		if p.Projects == nil {
			return domain.Session{}, 0, 0, apierr.Invalid("FLEET_HOME_REQUIRED", "An orchestrator session needs a project home node and checkout", nil)
		}
		home, err := p.Projects.Home(ctx, cfg.ProjectID)
		if err != nil {
			return domain.Session{}, 0, 0, err
		}
		req.HomeNode = home.NodeID
		req.HomeCheckout = home.Checkout
	}
	spawn := SpawnRequest{
		ProjectID:   string(cfg.ProjectID),
		Kind:        req.Kind,
		Harness:     string(cfg.Harness),
		Mode:        string(cfg.RequestedMode),
		Branch:      cfg.Branch,
		Prompt:      cfg.Prompt,
		DisplayName: cfg.DisplayName,
		ParentID:    string(cfg.ParentSessionID),
		Launch:      &cfg,
		RequestHash: cfg.ClientRequestHash,
	}
	// The session id is assigned by the worker's own engine today. Until that
	// engine reports it back, the placement is keyed by the client request so a
	// retry finds the same decision. A caller without one gets a fresh key.
	sessionKey := strings.TrimSpace(cfg.ClientRequestID)
	if sessionKey == "" {
		sessionKey = "fleet-" + newStreamID()
	}
	req.SessionID = sessionKey
	spawn.SessionID = sessionKey
	decision, err := p.Fleet.PlaceAndDispatch(ctx, req, spawn)
	if err != nil {
		return domain.Session{}, 0, 0, placementError(err)
	}
	record := domain.SessionRecord{
		ID:          domain.SessionID(sessionKey),
		ProjectID:   cfg.ProjectID,
		Kind:        domain.SessionKind(req.Kind),
		Harness:     cfg.Harness,
		DisplayName: cfg.DisplayName,
		Activity:    domain.Activity{State: domain.ActivityIdle},
		Mode:        cfg.RequestedMode,
	}
	return domain.Session{SessionRecord: record, StatusReadiness: string(decision.State), OwnerNodeID: decision.NodeID, ParentSessionID: cfg.ParentSessionID, PlacementState: string(decision.State)}, len(cfg.Prompt), 0, nil
}

func placementError(err error) error {
	switch {
	case errors.Is(err, ErrRequestConflict):
		return apierr.Conflict("CLIENT_REQUEST_CONFLICT", err.Error(), nil)
	case errors.Is(err, ErrHomeRequired):
		return apierr.Invalid("FLEET_HOME_REQUIRED", err.Error(), nil)
	case errors.Is(err, ErrNoNode), errors.Is(err, ErrNodeUnavailable), errors.Is(err, ErrControlExcluded):
		return apierr.Conflict("FLEET_NODE_UNAVAILABLE", err.Error(), nil)
	default:
		return fmt.Errorf("place fleet session: %w", err)
	}
}
