package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
)

// IdentityFunc reports the network identity of a dialing peer. It is optional.
// When a node was enrolled with an expected identity, the observed value must
// match it. The function is supplied by the deployment; Tailscale whois is one
// possible implementation and is not built in.
type IdentityFunc func(r *http.Request) (string, error)

// Listener accepts worker dial-ins on an address the operator configured.
// An empty address means the fleet listener is off.
type Listener struct {
	Addr        string
	Store       *Store
	Hub         *Hub
	Identity    IdentityFunc
	Log         *slog.Logger
	OnConnect   func(context.Context, string) error
	OnHeartbeat func(context.Context, string) error

	mu        sync.RWMutex
	bound     string
	onControl func(context.Context, Event) json.RawMessage
}

// SetControlHandler installs authenticated reverse RPC before daemon APIs serve.
func (l *Listener) SetControlHandler(fn func(context.Context, Event) json.RawMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onControl = fn
}

// Address is the address actually bound. It is empty until Run has listened,
// which is how a test discovers an ephemeral port.
func (l *Listener) Address() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.bound
}

// Run accepts authenticated worker dial-ins until ctx is cancelled.
func (l *Listener) Run(ctx context.Context) error {
	addr := strings.TrimSpace(l.Addr)
	if addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.bound = ln.Addr().String()
	l.mu.Unlock()
	srv := &http.Server{Handler: http.HandlerFunc(l.serve), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (l *Listener) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/fleet/v1/connect" {
		http.NotFound(w, r)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		l.logf("fleet websocket accept failed", "err", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var hello Frame
	if err := readFrame(ctx, conn, &hello); err != nil || hello.Type != FrameHello {
		_ = writeFrame(ctx, conn, Frame{Type: FrameRejected})
		return
	}
	var body Hello
	if err := json.Unmarshal(hello.Payload, &body); err != nil {
		_ = writeFrame(ctx, conn, Frame{Type: FrameRejected})
		return
	}
	observed := body.Identity
	if l.Identity != nil {
		observed, err = l.Identity(r)
		if err != nil {
			_ = writeFrame(ctx, conn, Frame{Type: FrameRejected})
			return
		}
	}
	node, err := l.Store.Authenticate(ctx, body.NodeID, body.Token, observed)
	if err != nil {
		l.logf("fleet node rejected", "node", body.NodeID, "err", err)
		_ = writeFrame(ctx, conn, Frame{Type: FrameRejected})
		return
	}
	if err := l.Store.Heartbeat(ctx, node.ID, body.Caps); err != nil {
		_ = writeFrame(ctx, conn, Frame{Type: FrameRejected})
		return
	}
	link := &socketConn{conn: conn}
	l.Hub.Attach(node.ID, link)
	defer l.Hub.Detach(node.ID, link)
	if err := link.write(ctx, Frame{Type: FrameAccepted, NodeID: node.ID}); err != nil {
		return
	}
	if l.OnConnect != nil {
		if err := l.OnConnect(ctx, node.ID); err != nil {
			l.logf("fleet pending launches wait for reconnect", "node", node.ID, "err", err)
		}
	}
	for {
		var frame Frame
		if err := readFrame(ctx, conn, &frame); err != nil {
			return
		}
		switch frame.Type {
		case FrameHeartbeat:
			if err := l.Store.Heartbeat(ctx, node.ID, frame.Caps); err != nil {
				return
			}
			if l.OnHeartbeat != nil {
				if err := l.OnHeartbeat(ctx, node.ID); err != nil {
					l.logf("fleet pending launch retry failed", "node", node.ID, "err", err)
				}
			}
		case FrameEvent:
			if frame.Event != nil {
				frame.Event.NodeID = node.ID
				if frame.Event.Kind == EventControlRequest {
					l.mu.RLock()
					handler := l.onControl
					l.mu.RUnlock()
					event := *frame.Event
					// The handler may issue downstream RPC on this socket; keep
					// reading its reply instead of deadlocking the connection.
					go func() {
						reply := EncodeSupervisionReply(nil, apierr.New(apierr.KindUnavailable, "FLEET_CONTROL_UNAVAILABLE", "Control supervision is not ready", nil))
						if handler != nil {
							reply = handler(ctx, event)
						}
						_ = link.Send(ctx, Command{ID: event.CommandID, Kind: CommandControlResponse, Payload: reply})
					}()
				} else {
					l.Hub.Publish(*frame.Event)
				}
			}
		case FrameTerminal:
			if frame.Terminal != nil {
				l.Hub.DeliverTerminal(*frame.Terminal)
			}
		default:
			return
		}
	}
}

func readFrame(ctx context.Context, conn *websocket.Conn, frame *Frame) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, frame)
}

func writeFrame(ctx context.Context, conn *websocket.Conn, frame Frame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

type socketConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (s *socketConn) write(ctx context.Context, frame Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFrame(ctx, s.conn, frame)
}

func (s *socketConn) Send(ctx context.Context, cmd Command) error {
	return s.write(ctx, Frame{Type: FrameCommand, Command: &cmd})
}

func (s *socketConn) SendTerminal(ctx context.Context, msg TerminalMessage) error {
	return s.write(ctx, Frame{Type: FrameTerminal, Terminal: &msg})
}

func (s *socketConn) Close() error {
	return s.conn.Close(websocket.StatusNormalClosure, "")
}

func (l *Listener) logf(msg string, args ...any) {
	if l.Log != nil {
		l.Log.Info(msg, args...)
	}
}
