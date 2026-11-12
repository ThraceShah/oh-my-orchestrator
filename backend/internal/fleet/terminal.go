package fleet

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

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
	defer conn.CloseNow()
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
			if err != nil && err != io.EOF {
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

// OpenSessionTerminal attaches the terminal the worker's own engine already
// started for the session. It does not launch an agent.
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
