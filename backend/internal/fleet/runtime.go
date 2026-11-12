package fleet

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Runtime is the control-plane half that a daemon starts when it is configured
// as one. A nil Runtime means this process is not a control plane.
type Runtime struct {
	Store    *Store
	Hub      *Hub
	Service  *Service
	Listener *Listener
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
	go applyEvents(ctx, hub, rt.Service, log)
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

func applyEvents(ctx context.Context, hub *Hub, svc *Service, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-hub.Events():
			var err error
			switch ev.Kind {
			case EventStarted:
				err = svc.NoteStarted(ctx, ev.SessionID, ev.NodeID)
			case EventFailed:
				err = svc.NoteFailed(ctx, ev.SessionID, ev.NodeID, string(ev.Payload))
			}
			if err != nil && log != nil && ctx.Err() == nil {
				log.Warn("fleet event was not recorded", "kind", ev.Kind, "err", err)
			}
		}
	}
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
