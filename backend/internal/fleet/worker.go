package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// Executor starts a session on this machine with the daemon's own session
// engine. The fleet worker is a client of that engine, not a second one.
type Executor interface {
	Spawn(context.Context, ports.SpawnConfig) (domain.Session, int, int, error)
}

// Messenger delivers a follow-up message to a session this machine already runs.
type Messenger interface {
	Send(context.Context, string, string) error
}

// PowerFunc runs one configured power action. The command text is deployment
// configuration; this function only receives the request.
type PowerFunc func(context.Context, PowerRequest) error

// WorkerHandler turns control-plane commands into local engine calls and
// returns an event the dialer sends back. A handler failure becomes an event
// with kind "failed" and does not change which node owns the session.
func WorkerHandler(exec Executor, power PowerFunc, log *slog.Logger) func(context.Context, Command) Event {
	return WorkerHandlerWithMessages(exec, nil, power, log)
}

// WorkerHandlerWithMessages is WorkerHandler plus delivery of a later message
// into a session this machine already started.
func WorkerHandlerWithMessages(exec Executor, messages Messenger, power PowerFunc, log *slog.Logger) func(context.Context, Command) Event {
	return func(ctx context.Context, cmd Command) Event {
		switch cmd.Kind {
		case CommandSpawn:
			return handleSpawn(ctx, exec, cmd)
		case CommandMessage:
			return handleMessage(ctx, messages, cmd)
		case CommandPower:
			return handlePower(ctx, power, cmd)
		default:
			if log != nil {
				log.Warn("fleet worker ignored command", "kind", cmd.Kind)
			}
			return Event{Kind: EventFailed, Payload: jsonRaw(fmt.Sprintf("unsupported command %s", cmd.Kind))}
		}
	}
}

func handleSpawn(ctx context.Context, exec Executor, cmd Command) Event {
	var req SpawnRequest
	if err := json.Unmarshal(cmd.Payload, &req); err != nil {
		return Event{Kind: EventFailed, Payload: jsonRaw(err.Error())}
	}
	if exec == nil {
		return Event{SessionID: req.SessionID, Kind: EventFailed, Payload: jsonRaw("this worker has no session engine")}
	}
	cfg := ports.SpawnConfig{
		ProjectID:       domain.ProjectID(req.ProjectID),
		ParentSessionID: domain.SessionID(req.ParentID),
		Kind:            domain.SessionKind(req.Kind),
		Harness:         domain.AgentHarness(req.Harness),
		RequestedMode:   domain.SessionMode(req.Mode),
		Branch:          req.Branch,
		Prompt:          req.Prompt,
		DisplayName:     req.DisplayName,
	}
	session, _, _, err := exec.Spawn(ctx, cfg)
	if err != nil {
		return Event{SessionID: req.SessionID, Kind: EventFailed, Payload: jsonRaw(err.Error())}
	}
	body, _ := json.Marshal(map[string]string{"sessionId": string(session.ID), "checkout": req.Checkout})
	// SessionID stays the control plane's placement key. The engine's id travels
	// in the payload because the worker's local session id is not the placement key.
	return Event{SessionID: req.SessionID, Kind: EventStarted, Payload: body}
}

func handleMessage(ctx context.Context, messages Messenger, cmd Command) Event {
	var body struct {
		SessionID string `json:"sessionId"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(cmd.Payload, &body); err != nil {
		return Event{Kind: EventFailed, Payload: jsonRaw(err.Error())}
	}
	if messages == nil {
		return Event{SessionID: body.SessionID, Kind: EventFailed, Payload: jsonRaw("this worker cannot deliver messages")}
	}
	if err := messages.Send(ctx, body.SessionID, body.Message); err != nil {
		return Event{SessionID: body.SessionID, Kind: EventFailed, Payload: jsonRaw(err.Error())}
	}
	return Event{SessionID: body.SessionID, Kind: "accepted"}
}

func handlePower(ctx context.Context, power PowerFunc, cmd Command) Event {
	req, err := DecodePower(cmd)
	if err != nil {
		return Event{Kind: EventFailed, Payload: jsonRaw(err.Error())}
	}
	if power == nil {
		return Event{Kind: EventFailed, Payload: jsonRaw("this node is not a power broker")}
	}
	if err := power(ctx, req); err != nil {
		return Event{Kind: EventFailed, Payload: jsonRaw(err.Error())}
	}
	body, _ := json.Marshal(req)
	return Event{Kind: EventPower, Payload: body}
}

func jsonRaw(message string) json.RawMessage {
	body, _ := json.Marshal(map[string]string{"message": message})
	return body
}

// LocalCapabilities reads the daemon's agent inventory. A harness counts as
// logged in only when its auth probe says so. An inventory error reports no
// capabilities, which makes the control plane refuse that harness rather than
// guess.
func LocalCapabilities(ctx context.Context, list func(context.Context) ([]string, []string, error)) ([]Capability, error) {
	if list == nil {
		return nil, nil
	}
	installed, authorized, err := list(ctx)
	if err != nil {
		return nil, err
	}
	auth := map[string]bool{}
	for _, id := range authorized {
		auth[id] = true
	}
	out := make([]Capability, 0, len(installed))
	for _, id := range installed {
		out = append(out, Capability{Harness: id, LoggedIn: auth[id]})
	}
	return out, nil
}
