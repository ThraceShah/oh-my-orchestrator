package fleet

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

const terminalHandlePrefix = "fleet-term:"

// TerminalHandle is the runtime handle a control-plane client opens on the
// ordinary terminal mux. The mux source recognizes it and relays the bytes.
func TerminalHandle(sessionID string) string {
	return terminalHandlePrefix + sessionID
}

// TerminalStream is the worker-local byte stream for one session. Resize is
// optional; streams that cannot resize ignore it.
type TerminalStream interface {
	io.ReadWriteCloser
}

// Bridge runs terminal streams on the worker. Open must return the session's
// existing terminal, not start a second agent.
type Bridge struct {
	Open func(context.Context, string, uint16, uint16) (TerminalStream, error)

	mu      sync.Mutex
	streams map[string]TerminalStream
}

// Handle applies one terminal frame. It returns before a stream's read loop
// finishes, so the dialer can keep reading other frames.
func (b *Bridge) Handle(ctx context.Context, msg TerminalMessage, send func(TerminalMessage) error) {
	if b == nil || b.Open == nil {
		_ = send(TerminalMessage{StreamID: msg.StreamID, Op: "error", Error: "this node has no terminal bridge"})
		return
	}
	switch msg.Op {
	case "open":
		stream, err := b.Open(ctx, msg.SessionID, msg.Cols, msg.Rows)
		if err != nil {
			_ = send(TerminalMessage{StreamID: msg.StreamID, Op: "error", Error: err.Error()})
			return
		}
		b.mu.Lock()
		if b.streams == nil {
			b.streams = map[string]TerminalStream{}
		}
		b.streams[msg.StreamID] = stream
		b.mu.Unlock()
		if err := send(TerminalMessage{StreamID: msg.StreamID, Op: "opened"}); err != nil {
			_ = stream.Close()
			return
		}
		go b.read(ctx, msg.StreamID, stream, send)
	case "data":
		raw, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			_ = send(TerminalMessage{StreamID: msg.StreamID, Op: "error", Error: "terminal data is not base64"})
			return
		}
		if stream := b.stream(msg.StreamID); stream != nil {
			_, _ = stream.Write(raw)
		}
	case "resize":
		if stream := b.stream(msg.StreamID); stream != nil {
			if sized, ok := stream.(interface{ Resize(uint16, uint16) error }); ok {
				_ = sized.Resize(msg.Rows, msg.Cols)
			}
		}
	case "close":
		if stream := b.take(msg.StreamID); stream != nil {
			_ = stream.Close()
		}
	}
}

func (b *Bridge) read(ctx context.Context, streamID string, stream TerminalStream, send func(TerminalMessage) error) {
	defer func() {
		_ = stream.Close()
		b.take(streamID)
		_ = send(TerminalMessage{StreamID: streamID, Op: "closed"})
	}()
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := stream.Read(buf)
		if n > 0 {
			if sendErr := send(TerminalMessage{StreamID: streamID, Op: "data", Data: base64.StdEncoding.EncodeToString(buf[:n])}); sendErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *Bridge) stream(id string) TerminalStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.streams[id]
}

func (b *Bridge) take(id string) TerminalStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	stream := b.streams[id]
	delete(b.streams, id)
	return stream
}

// Relay is the control-plane end of a terminal. A loopback client opens one
// session; the relay forwards bytes to the worker that owns it.
type Relay struct {
	Hub   *Hub
	Store *Store
}

type terminalClientMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	Data      string `json:"data,omitempty"`
	Cols      uint16 `json:"cols,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r == nil || r.Hub == nil || r.Store == nil {
		http.Error(w, "fleet terminal is not available", http.StatusNotFound)
		return
	}
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx := req.Context()
	var open terminalClientMessage
	if err := readClient(ctx, conn, &open); err != nil || open.Type != "open" || open.SessionID == "" {
		_ = writeClient(ctx, conn, terminalClientMessage{Type: "error", Error: "the first frame must open a session"})
		return
	}
	placement, ok, err := r.Store.PlacementFor(ctx, open.SessionID)
	if err != nil || !ok || placement.NodeID == "" {
		_ = writeClient(ctx, conn, terminalClientMessage{Type: "error", Error: "session is not placed on a fleet node"})
		return
	}
	streamID := newStreamID()
	replies, cancel := r.Hub.SubscribeTerminal(streamID)
	defer func() {
		_ = r.Hub.SendTerminal(context.Background(), placement.NodeID, TerminalMessage{StreamID: streamID, Op: "close"})
		cancel()
	}()
	engine := placement.EngineID
	if engine == "" {
		engine = placement.SessionID
	}
	if err := r.Hub.SendTerminal(ctx, placement.NodeID, TerminalMessage{StreamID: streamID, Op: "open", SessionID: engine, Cols: open.Cols, Rows: open.Rows}); err != nil {
		_ = writeClient(ctx, conn, terminalClientMessage{Type: "error", Error: err.Error()})
		return
	}
	errCh := make(chan error, 1)
	go func() {
		for {
			var msg terminalClientMessage
			if err := readClient(ctx, conn, &msg); err != nil {
				errCh <- err
				return
			}
			op := msg.Type
			if op != "data" && op != "resize" && op != "close" {
				continue
			}
			if err := r.Hub.SendTerminal(ctx, placement.NodeID, TerminalMessage{StreamID: streamID, Op: op, Data: msg.Data, Cols: msg.Cols, Rows: msg.Rows}); err != nil {
				errCh <- err
				return
			}
			if op == "close" {
				errCh <- io.EOF
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if err != nil && !errors.Is(err, io.EOF) {
				_ = writeClient(ctx, conn, terminalClientMessage{Type: "error", Error: err.Error()})
			}
			return
		case msg, ok := <-replies:
			if !ok {
				return
			}
			out := terminalClientMessage{Type: msg.Op, Data: msg.Data, Error: msg.Error}
			if err := writeClient(ctx, conn, out); err != nil {
				return
			}
			if msg.Op == "error" || msg.Op == "closed" {
				return
			}
		}
	}
}

func readClient(ctx context.Context, conn *websocket.Conn, msg *terminalClientMessage) error {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, msg)
}

func writeClient(ctx context.Context, conn *websocket.Conn, msg terminalClientMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func newStreamID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// LocalTerminal is the control plane's own terminal source. Fleet handles
// are relayed; every other handle stays on the local runtime.
type LocalTerminal interface {
	Attach(context.Context, ports.RuntimeHandle, uint16, uint16) (ports.Stream, error)
	IsAlive(context.Context, ports.RuntimeHandle) (bool, error)
}

// MuxSource lets the existing /mux terminal protocol reach a placed session.
// The desktop keeps opening a runtime handle. A fleet-term handle is not a
// local process.
type MuxSource struct {
	Inner LocalTerminal
	Hub   *Hub
	Store *Store
}

// IsAlive observes the owner link without treating a disconnection as death.
func (m MuxSource) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	sessionID, ok := sessionFromTerminalHandle(handle.ID)
	if !ok {
		if m.Inner == nil {
			return false, errors.New("no local terminal")
		}
		return m.Inner.IsAlive(ctx, handle)
	}
	if m.Store == nil {
		return false, errors.New("fleet terminal is not available")
	}
	placement, found, err := m.Store.PlacementFor(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if m.Hub == nil || !m.Hub.Connected(placement.NodeID) {
		return false, fmt.Errorf("fleet node %s is not connected", placement.NodeID)
	}
	return placement.State == PlacementRunning || placement.State == PlacementStarting, nil
}

// Attach opens the existing local or worker-owned session terminal.
func (m MuxSource) Attach(ctx context.Context, handle ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	sessionID, ok := sessionFromTerminalHandle(handle.ID)
	if !ok {
		if m.Inner == nil {
			return nil, errors.New("no local terminal")
		}
		return m.Inner.Attach(ctx, handle, rows, cols)
	}
	if m.Hub == nil || m.Store == nil {
		return nil, errors.New("fleet terminal is not available")
	}
	placement, found, err := m.Store.PlacementFor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !found || placement.NodeID == "" {
		return nil, fmt.Errorf("session %s is not placed on a fleet node", sessionID)
	}
	engine := placement.EngineID
	if engine == "" {
		engine = placement.SessionID
	}
	streamID := newStreamID()
	replies, cancel := m.Hub.SubscribeTerminal(streamID)
	if err := m.Hub.SendTerminal(ctx, placement.NodeID, TerminalMessage{StreamID: streamID, Op: "open", SessionID: engine, Cols: cols, Rows: rows}); err != nil {
		cancel()
		return nil, err
	}
	select {
	case msg, ok := <-replies:
		if !ok {
			cancel()
			return nil, errors.New("fleet terminal closed before it opened")
		}
		if msg.Op == "error" {
			cancel()
			return nil, errors.New(msg.Error)
		}
		if msg.Op != "opened" {
			cancel()
			return nil, fmt.Errorf("fleet terminal answered %s", msg.Op)
		}
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	read, write := io.Pipe()
	go copyTerminal(replies, write)
	return &muxStream{reader: read, write: write, cancel: cancel, send: func(op, data string, c, r uint16) error {
		return m.Hub.SendTerminal(context.Background(), placement.NodeID, TerminalMessage{StreamID: streamID, Op: op, Data: data, Cols: c, Rows: r})
	}}, nil
}

type muxStream struct {
	reader io.ReadCloser
	write  *io.PipeWriter
	cancel func()
	send   func(op, data string, cols, rows uint16) error
	once   sync.Once
}

func (s *muxStream) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *muxStream) Write(p []byte) (int, error) {
	if err := s.send("data", base64.StdEncoding.EncodeToString(p), 0, 0); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *muxStream) Resize(rows, cols uint16) error { return s.send("resize", "", cols, rows) }

func (s *muxStream) Close() error {
	var err error
	s.once.Do(func() {
		err = s.send("close", "", 0, 0)
		s.cancel()
		_ = s.write.Close()
		_ = s.reader.Close()
	})
	return err
}

func copyTerminal(replies <-chan TerminalMessage, out *io.PipeWriter) {
	defer func() { _ = out.Close() }()
	for msg := range replies {
		switch msg.Op {
		case "data":
			raw, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				return
			}
			if _, err := out.Write(raw); err != nil {
				return
			}
		case "closed", "error":
			return
		}
	}
}

func sessionFromTerminalHandle(handle string) (string, bool) {
	sessionID, ok := strings.CutPrefix(handle, terminalHandlePrefix)
	return sessionID, ok && sessionID != ""
}

// OpenSessionTerminal attaches the terminal the worker engine already started;
// it never launches another agent.
func OpenSessionTerminal(ctx context.Context, handleOf func(context.Context, string) (string, error), attach ports.Attacher, sessionID string, cols, rows uint16) (ports.Stream, error) {
	if handleOf == nil || attach == nil {
		return nil, fmt.Errorf("session %s has no terminal", sessionID)
	}
	handle, err := handleOf(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if handle == "" {
		return nil, fmt.Errorf("session %s has no terminal", sessionID)
	}
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return attach.Attach(ctx, ports.RuntimeHandle{ID: handle}, rows, cols)
}
