package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	sessionmanager "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/session_manager"
)

type crossNodeMessage struct {
	Target domain.SessionID `json:"targetSessionId"`
	fleet.SessionMessage
}

type fleetSessionLookup interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

// A worker sends local targets directly to its own engine. Only an unknown
// global target is routed upstream; a local failure must never be retried on a
// different engine.
type workerUpstreamSupervision struct {
	dialer *fleet.Dialer
	store  fleetSessionLookup
}

func (workerUpstreamSupervision) GetSession(context.Context, domain.SessionID) (domain.Session, bool, error) {
	return domain.Session{}, false, nil
}
func (workerUpstreamSupervision) KillSession(context.Context, domain.SessionID) (bool, bool, error) {
	return false, false, nil
}
func (workerUpstreamSupervision) RequestKillSession(context.Context, domain.SessionID) (sessionmanager.KillResult, bool, error) {
	return sessionmanager.KillResult{}, false, nil
}
func (w workerUpstreamSupervision) SendSession(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment, options ports.MessageDeliveryOptions) (bool, error) {
	_, found, err := w.store.GetSession(ctx, id)
	if err != nil || found {
		return false, err
	}
	if options.SenderSessionID == "" {
		return true, apierr.Invalid("FLEET_SENDER_REQUIRED", "Cross-node messages require a sending session", nil)
	}
	sender, found, err := w.store.GetSession(ctx, domain.SessionID(options.SenderSessionID))
	if err != nil {
		return true, err
	}
	if !found || sender.Kind != domain.KindWorker {
		return true, apierr.Forbidden("FLEET_SENDER_INVALID", "The sending session is not a local worker")
	}
	options.AuthoredByUser = false
	return true, w.dialer.CallControl(ctx, "session_send", crossNodeMessage{Target: id, SessionMessage: fleet.SessionMessage{Message: message, Attachment: attachment, Options: options}}, nil)
}

// controlWorkerRequests accepts only a session actually owned by the
// authenticated dialing node and keeps every message in its original project.
func controlWorkerRequests(rt *fleet.Runtime, sessions interface {
	Get(context.Context, domain.SessionID) (domain.Session, error)
	SendWithOptions(context.Context, domain.SessionID, string, *ports.SpawnAttachment, ports.MessageDeliveryOptions) error
}) func(context.Context, fleet.Event) json.RawMessage {
	return func(ctx context.Context, event fleet.Event) json.RawMessage {
		var request fleet.ControlRequest
		if err := json.Unmarshal(event.Payload, &request); err != nil {
			return fleet.EncodeSupervisionReply(nil, apierr.Invalid("INVALID_FLEET_REQUEST", "Invalid worker request", nil))
		}
		deadline := time.Now().Add(15 * time.Second)
		if !request.ExpiresAt.IsZero() && request.ExpiresAt.Before(deadline) {
			deadline = request.ExpiresAt
		}
		if !deadline.After(time.Now()) {
			return fleet.EncodeSupervisionReply(nil, apierr.Unavailable("FLEET_REQUEST_EXPIRED", "The cross-node message request expired"))
		}
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		if request.Op != "session_send" {
			return fleet.EncodeSupervisionReply(nil, apierr.NotImplemented("FLEET_OPERATION_UNSUPPORTED", "Unsupported control operation"))
		}
		var message crossNodeMessage
		if err := json.Unmarshal(request.Payload, &message); err != nil {
			return fleet.EncodeSupervisionReply(nil, apierr.Invalid("INVALID_MESSAGE", "Invalid cross-node message", nil))
		}
		source, found, err := rt.Store.PlacementForNode(ctx, event.NodeID, message.Options.SenderSessionID)
		if err != nil {
			return fleet.EncodeSupervisionReply(nil, err)
		}
		if !found || source.Kind != string(domain.KindWorker) {
			return fleet.EncodeSupervisionReply(nil, apierr.Forbidden("FLEET_SENDER_INVALID", "The session is not owned by this worker"))
		}
		target, err := sessions.Get(ctx, message.Target)
		if err != nil {
			return fleet.EncodeSupervisionReply(nil, err)
		}
		if string(target.ProjectID) != source.ProjectID {
			return fleet.EncodeSupervisionReply(nil, apierr.Forbidden("FLEET_PROJECT_MISMATCH", "Cross-node messages must stay in the sending session's project"))
		}
		message.Options.SenderSessionID = source.SessionID
		message.Options.AuthoredByUser = false
		err = sessions.SendWithOptions(ctx, message.Target, message.Message, message.Attachment, message.Options)
		return fleet.EncodeSupervisionReply(nil, err)
	}
}
