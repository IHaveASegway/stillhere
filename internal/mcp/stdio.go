package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP's stdio transport is newline-delimited JSON-RPC: each message is one
// line, with no newline inside it. The SDK's reader decodes the stream as
// a whole, so one line that is not a message ends the session, and with it
// the process, while JSON-RPC 2.0 asks a server to answer such a line with
// an error and go on serving. lineGuard sits between stdin and the SDK and
// does that: it hands the SDK whole lines that are messages, and answers
// any other line itself. It is a tolerant reader at the edge (Postel's
// robustness principle, RFC 1122 §1.2.2) that keeps the protocol strict
// inside.
//
// It also drains (#58). When stdin ends, the SDK stops at once: it cancels
// the calls in flight and writes no answer after it, so a client that pipes
// its requests and closes stdin (printf '…' | stillhere mcp) got nothing
// back, not even initialize's answer. lineGuard keeps the end of stdin from
// the SDK until every call it handed over has been answered, then lets it
// end, as an HTTP server's graceful shutdown finishes the requests it has
// accepted before it closes (net/http's Server.Shutdown).

// lockedWriter serialises the writes to stdout of the SDK and of lineGuard,
// each of which writes one whole line per call, and tells the guard which
// calls each line answers.
type lockedWriter struct {
	mu    sync.Mutex
	w     io.Writer
	guard *lineGuard // nil: nothing to tell
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.w.Write(p)
	if l.guard != nil {
		l.guard.wrote(p, err)
	}
	return n, err
}

// lineGuard is the io.ReadCloser the SDK reads the client's messages from.
type lineGuard struct {
	ctx   context.Context // the session's: the drain stops waiting when it ends
	in    io.ReadCloser
	r     *bufio.Reader
	out   io.Writer
	log   *slog.Logger
	limit int    // the longest line it reads: the SDK's own cap on a message
	buf   []byte // the rest of the line being handed to the SDK

	mu      sync.Mutex
	pending map[jsonrpc.ID]bool // calls handed to the SDK and not answered yet
	broken  bool                // a write to stdout failed: no answer can still arrive
	changed chan struct{}       // a pending call was answered, or a write failed
}

func newLineGuard(ctx context.Context, in io.ReadCloser, out io.Writer, log *slog.Logger) *lineGuard {
	return &lineGuard{ctx: ctx, in: in, r: bufio.NewReaderSize(in, 64<<10), out: out, log: log,
		limit: sdk.DefaultMaxLineLength, pending: map[jsonrpc.ID]bool{}, changed: make(chan struct{}, 1)}
}

// Read hands the SDK the next message line, whole and ending in a newline.
// At the end of stdin it first waits for the calls in flight to be
// answered (drain).
func (g *lineGuard) Read(p []byte) (int, error) {
	for len(g.buf) == 0 {
		line, err := g.line()
		if line != nil {
			g.buf = line
		}
		if err != nil && len(g.buf) == 0 {
			g.drain()
			return 0, err
		}
		if err != nil {
			break
		}
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

// Close closes stdin.
func (g *lineGuard) Close() error { return g.in.Close() }

// line reads one line and returns it when it is a message, trimmed of
// surrounding white space and ending in a newline; otherwise it answers
// the line and returns nil. The error is the reader's, such as io.EOF
// after the last line.
func (g *lineGuard) line() ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := g.r.ReadSlice('\n')
		if !tooLong {
			line = append(line, chunk...)
			if len(line) > g.limit {
				tooLong, line = true, nil
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			g.reject(jsonrpc.CodeInvalidRequest, fmt.Sprintf("Invalid Request: a message is longer than %d bytes", g.limit))
			return nil, err
		}
		msg := bytes.TrimSpace(line)
		if len(msg) == 0 {
			return nil, err
		}
		msgs, code, why := check(msg)
		if code != 0 {
			g.reject(code, why)
			return nil, err
		}
		g.handed(msgs)
		return append(msg, '\n'), err
	}
}

// check decodes a line as a JSON-RPC message or batch. It returns the
// messages, or why the line is none, with JSON-RPC's code for it.
func check(msg []byte) ([]jsonrpc.Message, int, string) {
	if !json.Valid(msg) {
		return nil, jsonrpc.CodeParseError, "Parse error: the line is not JSON"
	}
	if msg[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(msg, &batch); err != nil || len(batch) == 0 {
			return nil, jsonrpc.CodeInvalidRequest, "Invalid Request: an empty batch"
		}
		var msgs []jsonrpc.Message
		for _, m := range batch {
			d, err := jsonrpc.DecodeMessage(m)
			if err != nil || !whole(d) {
				return nil, jsonrpc.CodeInvalidRequest, "Invalid Request: a batch holds something that is not a JSON-RPC 2.0 message"
			}
			msgs = append(msgs, d)
		}
		return msgs, 0, ""
	}
	d, err := jsonrpc.DecodeMessage(msg)
	switch {
	case err != nil:
		return nil, jsonrpc.CodeInvalidRequest, "Invalid Request: the line is not a JSON-RPC 2.0 message"
	case !whole(d):
		// An id with neither a method nor a result or an error is no
		// request and no response: the SDK would take it for an answer to
		// a call it never made, and drop it without a word (MC-10).
		return nil, jsonrpc.CodeInvalidRequest, "Invalid Request: the message has an id but no method, result or error"
	}
	return []jsonrpc.Message{d}, 0, ""
}

// whole reports whether a decoded message is a request, a notification, or
// a response that holds its result or its error, as JSON-RPC 2.0 requires.
func whole(m jsonrpc.Message) bool {
	r, ok := m.(*jsonrpc.Response)
	return !ok || len(r.Result) > 0 || r.Error != nil
}

// reject answers a line that is not a message: its id cannot be read, so
// the answer's id is null, as JSON-RPC 2.0 has it.
func (g *lineGuard) reject(code int, why string) {
	g.log.Warn("skipped a line from the client that is not a JSON-RPC message", "code", code, "why", why)
	resp, err := json.Marshal(struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      *int          `json:"id"`
		Error   jsonrpc.Error `json:"error"`
	}{JSONRPC: "2.0", Error: jsonrpc.Error{Code: int64(code), Message: why}})
	if err != nil {
		return
	}
	if _, err := g.out.Write(append(resp, '\n')); err != nil {
		g.log.Warn("answering a line that is not a message", "error", err)
	}
}

// handed records the calls of a line handed to the SDK, which owe an
// answer, and forgets a call the client cancelled: MCP has the server send
// no answer to it.
func (g *lineGuard) handed(msgs []jsonrpc.Message) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, m := range msgs {
		req, ok := m.(*jsonrpc.Request)
		switch {
		case !ok:
		case req.IsCall():
			// A call whose id is still in flight is refused by the SDK
			// without an answer it can send; recording it once keeps the
			// drain from waiting for that one.
			g.pending[req.ID] = true
		case req.Method == "notifications/cancelled":
			var p struct {
				RequestID any `json:"requestId"`
			}
			if json.Unmarshal(req.Params, &p) == nil {
				if id, err := jsonrpc.MakeID(p.RequestID); err == nil {
					delete(g.pending, id)
					g.signal()
				}
			}
		}
	}
}

// wrote forgets the calls a line written to stdout answers.
func (g *lineGuard) wrote(line []byte, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.broken = true
		g.signal()
		return
	}
	msgs, code, _ := check(bytes.TrimSpace(line))
	if code != 0 {
		return
	}
	for _, m := range msgs {
		if r, ok := m.(*jsonrpc.Response); ok && g.pending[r.ID] {
			delete(g.pending, r.ID)
			g.signal()
		}
	}
}

// signal wakes the drain, if it waits; the caller holds mu.
func (g *lineGuard) signal() {
	select {
	case g.changed <- struct{}{}:
	default:
	}
}

// drain waits until every call handed to the SDK has been answered, stdout
// has failed, or the session's context ends.
func (g *lineGuard) drain() {
	for {
		g.mu.Lock()
		n, broken := len(g.pending), g.broken
		g.mu.Unlock()
		if n == 0 || broken {
			return
		}
		select {
		case <-g.changed:
		case <-g.ctx.Done():
			return
		}
	}
}
