package session

import (
	"context"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	sessionmanager "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/session_manager"
)

// FleetSupervision resolves only globally addressed sessions owned by another
// machine. A handled error must never fall through to the local engine.
type FleetSupervision interface {
	GetSession(context.Context, domain.SessionID) (domain.Session, bool, error)
	KillSession(context.Context, domain.SessionID) (bool, bool, error)
	RequestKillSession(context.Context, domain.SessionID) (sessionmanager.KillResult, bool, error)
	SendSession(context.Context, domain.SessionID, string, *ports.SpawnAttachment, ports.MessageDeliveryOptions) (bool, error)
}

// SetFleetSupervision installs the remote read/lifecycle boundary before serving.
func (s *Service) SetFleetSupervision(remote FleetSupervision) { s.fleetSupervision = remote }
