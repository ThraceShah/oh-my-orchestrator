package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"
)

// Runtime is the control-plane half that a daemon starts when it is configured
// as one. A nil Runtime means this process is not a control plane.
type Runtime struct {
	Store    *Store
	Hub      *Hub
	Service  *Service
	Listener *Listener
	live     atomic.Pointer[func(Event)]
}

// NotifyLive receives chat-changed events from workers. The callback must not
// block. A nil callback drops the event.
func (rt *Runtime) NotifyLive(fn func(Event)) {
	if rt == nil {
		return
	}
	rt.live.Store(&fn)
}

// StartControl opens the fleet tables and, when addr is set, accepts worker
// dial-ins until ctx ends. The listener runs in the background.
func StartControl(ctx context.Context, db *sql.DB, addr string, identity IdentityFunc, log *slog.Logger) (*Runtime, error) {
	store := NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return nil, err
	}
	hub := NewHub()
	rt := &Runtime{
		Store:   store,
		Hub:     hub,
		Service: NewService(store, hub),
		Listener: &Listener{
			Addr:     addr,
			Store:    store,
			Hub:      hub,
			Identity: identity,
			Log:      log,
		},
	}
	rt.Listener.OnConnect = rt.Service.ResumeNode
	rt.Listener.OnHeartbeat = rt.Service.ResumePending
	go applyEvents(ctx, hub, rt.Service, log, &rt.live)
	if addr != "" {
		go func() {
			if err := rt.Listener.Run(ctx); err != nil && ctx.Err() == nil && log != nil {
				log.Error("fleet listener stopped", "err", err)
			}
		}()
		go expire(ctx, rt.Service, log)
	}
	return rt, nil
}

func applyEvents(ctx context.Context, hub *Hub, svc *Service, log *slog.Logger, live *atomic.Pointer[func(Event)]) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-hub.Events():
			var err error
			switch ev.Kind {
			case EventStarted:
				err = svc.NoteStarted(ctx, ev.SessionID, ev.NodeID, engineID(ev), engineMode(ev))
			case EventFailed:
				err = svc.NoteFailed(ctx, ev.SessionID, ev.NodeID, string(ev.Payload))
			case EventChatChanged:
				if live != nil {
					if fn := live.Load(); fn != nil && *fn != nil {
						(*fn)(ev)
					}
				}
			}
			if err != nil && log != nil && ctx.Err() == nil {
				log.Warn("fleet event was not recorded", "kind", ev.Kind, "err", err)
			}
		}
	}
}

// ChatChanged reports a worker conversation update the control plane can
// forward. Updates without a conversation id are ignored.
func ChatChanged(sessionID string, payload json.RawMessage) (Event, bool) {
	if sessionID == "" {
		return Event{}, false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(payload, &body) != nil {
		return Event{}, false
	}
	if _, ok := body["conversationId"]; !ok {
		return Event{}, false
	}
	return Event{SessionID: sessionID, Kind: EventChatChanged, Payload: append(json.RawMessage(nil), payload...)}, true
}

func engineID(ev Event) string {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(ev.Payload, &body)
	return body.SessionID
}

func engineMode(ev Event) string {
	var body struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(ev.Payload, &body)
	return body.Mode
}

func expire(ctx context.Context, svc *Service, log *slog.Logger) {
	ticker := time.NewTicker(DefaultHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := svc.ObserveOffline(ctx); err != nil && log != nil && ctx.Err() == nil {
				log.Warn("fleet heartbeat expiry failed", "err", err)
			}
		}
	}
}
