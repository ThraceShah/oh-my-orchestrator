package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	sessionmanager "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/session_manager"
)

// PlacementStopped records acknowledged terminal intent, not a display status.
const PlacementStopped Placement = "stopped"

func canonicalChatSessionIDs(raw json.RawMessage, p SessionPlacement) json.RawMessage {
	if p.EngineID == "" || p.EngineID == p.SessionID {
		return raw
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return raw
	}
	var walk func(any)
	walk = func(value any) {
		switch object := value.(type) {
		case map[string]any:
			for key, child := range object {
				if key == "SessionID" || key == "sessionId" || strings.HasSuffix(key, "SessionID") || strings.HasSuffix(key, "SessionId") {
					if id, ok := child.(string); ok && id == p.EngineID {
						object[key] = p.SessionID
						continue
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range object {
				walk(child)
			}
		}
	}
	walk(value)
	result, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return result
}

// SupervisionError preserves the worker service's public error boundary without
// exposing an arbitrary internal error or serializing a Go error cause.
type SupervisionError struct {
	Kind    apierr.Kind    `json:"kind"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// SupervisionReply carries either a worker result or its public service error.
type SupervisionReply struct {
	Data  json.RawMessage   `json:"data,omitempty"`
	Error *SupervisionError `json:"error,omitempty"`
}

// EncodeSupervisionReply is shared by worker operations and reverse requests.
func EncodeSupervisionReply(value any, err error) json.RawMessage {
	reply := SupervisionReply{}
	if err != nil {
		var public *apierr.Error
		if !errors.As(err, &public) {
			public = apierr.Internal("FLEET_OPERATION_FAILED", "The remote operation failed")
		}
		reply.Error = &SupervisionError{Kind: public.Kind, Code: public.Code, Message: public.Message, Details: public.Details}
	} else {
		reply.Data, err = json.Marshal(value)
		if err != nil {
			return EncodeSupervisionReply(nil, err)
		}
	}
	raw, _ := json.Marshal(reply)
	return raw
}

// DecodeSupervisionReply decodes a result and restores its public error kind.
func DecodeSupervisionReply(raw json.RawMessage, value any) error {
	var reply SupervisionReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return apierr.InternalCause("FLEET_INVALID_RESPONSE", "Invalid worker response", err)
	}
	if reply.Error != nil {
		return apierr.New(reply.Error.Kind, reply.Error.Code, reply.Error.Message, reply.Error.Details)
	}
	if value != nil {
		if len(reply.Data) == 0 {
			return apierr.Internal("FLEET_INVALID_RESPONSE", "The worker returned no result")
		}
		if err := json.Unmarshal(reply.Data, value); err != nil {
			return apierr.InternalCause("FLEET_INVALID_RESPONSE", "Invalid worker result", err)
		}
	}
	return nil
}

func placementFailureReason(detail string) string {
	var failure struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(detail), &failure) == nil && strings.TrimSpace(failure.Message) != "" {
		return strings.TrimSpace(failure.Message)
	}
	return strings.TrimSpace(detail)
}

// supervision calls only the placement's owning engine, never another worker.
func (s *Service) supervision(ctx context.Context, p SessionPlacement, op string, payload, result any) error {
	// A durable start failure remains a failure even after the worker disconnects.
	// It must not be described as an operation still waiting for an engine id.
	if p.EngineID == "" && p.State == PlacementFailed {
		reason := placementFailureReason(p.Detail)
		message := "The worker could not start the session"
		if reason != "" {
			message += ": " + reason
		}
		return apierr.New(apierr.KindUnavailable, "FLEET_SESSION_FAILED", message, map[string]any{"nodeId": p.NodeID, "placementState": p.State, "reason": reason})
	}
	if s.hub == nil || !s.hub.Connected(p.NodeID) {
		return apierr.New(apierr.KindUnavailable, "FLEET_NODE_OFFLINE", "The session's worker is offline", map[string]any{"nodeId": p.NodeID})
	}
	if p.EngineID == "" {
		return apierr.New(apierr.KindUnavailable, "FLEET_SESSION_STARTING", "The worker has not reported a session yet", map[string]any{"placementState": p.State})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := json.Marshal(struct {
		SessionID string          `json:"sessionId"`
		Op        string          `json:"op"`
		Payload   json.RawMessage `json:"payload"`
	}{p.EngineID, op, body})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	event, err := s.hub.Call(ctx, p.NodeID, Command{Kind: CommandChat, Payload: request})
	if err != nil {
		return apierr.New(apierr.KindUnavailable, "FLEET_WORKER_UNAVAILABLE", "The worker could not answer the operation", map[string]any{"nodeId": p.NodeID})
	}
	if event.Kind == EventFailed {
		return apierr.Internal("FLEET_OPERATION_FAILED", "The worker could not execute this operation")
	}
	return DecodeSupervisionReply(event.Payload, result)
}

func (s *Service) remotePlacement(ctx context.Context, id domain.SessionID) (SessionPlacement, bool, error) {
	if s == nil || s.store == nil {
		return SessionPlacement{}, false, nil
	}
	// Only permanent global identities are public addresses. A worker engine id
	// can collide with an unrelated local or remote session.
	return s.store.Placement(ctx, string(id))
}

// GetSession reads a globally addressed session from its owning worker.
func (s *Service) GetSession(ctx context.Context, id domain.SessionID) (domain.Session, bool, error) {
	p, found, err := s.remotePlacement(ctx, id)
	if !found || err != nil {
		return domain.Session{}, found, err
	}
	var session domain.Session
	if err := s.supervision(ctx, p, "session_get", nil, &session); err != nil {
		return domain.Session{}, true, err
	}
	return s.canonicalSession(p, session), true, nil
}

func (s *Service) canonicalSession(p SessionPlacement, session domain.Session) domain.Session {
	session.ID = domain.SessionID(p.SessionID)
	session.ProjectID = domain.ProjectID(p.ProjectID)
	session.ParentSessionID = domain.SessionID(p.ParentID)
	session.OwnerNodeID = p.NodeID
	session.PlacementState = string(p.State)
	// Chat sessions have no terminal. Never invent a terminal handle for them.
	if session.TerminalHandleID != "" {
		session.TerminalHandleID = TerminalHandle(p.SessionID)
	}
	return session
}

// KillSession waits for the owning worker to acknowledge session teardown.
func (s *Service) KillSession(ctx context.Context, id domain.SessionID) (bool, bool, error) {
	p, found, err := s.remotePlacement(ctx, id)
	if !found || err != nil {
		return false, found, err
	}
	var freed bool
	err = s.supervision(ctx, p, "session_kill", nil, &freed)
	if err == nil {
		p.State = PlacementStopped
		p.UpdatedAt = s.now().UTC()
		err = s.store.PutPlacement(ctx, p)
	}
	return freed, true, err
}

// RequestKillSession records terminal intent without waiting for workspace cleanup.
func (s *Service) RequestKillSession(ctx context.Context, id domain.SessionID) (sessionmanager.KillResult, bool, error) {
	p, found, err := s.remotePlacement(ctx, id)
	if !found || err != nil {
		return sessionmanager.KillResult{}, found, err
	}
	var result sessionmanager.KillResult
	err = s.supervision(ctx, p, "session_request_kill", nil, &result)
	if err == nil {
		p.State = PlacementStopped
		p.UpdatedAt = s.now().UTC()
		err = s.store.PutPlacement(ctx, p)
	}
	return result, true, err
}

// SessionMessage preserves message authorship and attachments across fleet RPC.
type SessionMessage struct {
	Message    string                       `json:"message"`
	Attachment *ports.SpawnAttachment       `json:"attachment,omitempty"`
	Options    ports.MessageDeliveryOptions `json:"options"`
}

// SendSession delivers to the owning worker and reports whether the id is remote.
func (s *Service) SendSession(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment, options ports.MessageDeliveryOptions) (bool, error) {
	p, found, err := s.remotePlacement(ctx, id)
	if !found || err != nil {
		return found, err
	}
	return true, s.supervision(ctx, p, "session_send", SessionMessage{message, attachment, options}, nil)
}
