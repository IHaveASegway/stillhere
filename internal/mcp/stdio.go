package mcp

import (
	"bufio"
	"bytes"
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

// lockedWriter serialises the writes to stdout of the SDK and of lineGuard,
// each of which writes one whole line per call.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// lineGuard is the io.ReadCloser the SDK reads the client's messages from.
type lineGuard struct {
	in    io.ReadCloser
	r     *bufio.Reader
	out   io.Writer
	log   *slog.Logger
	limit int    // the longest line it reads: the SDK's own cap on a message
	buf   []byte // the rest of the line being handed to the SDK
}

func newLineGuard(in io.ReadCloser, out io.Writer, log *slog.Logger) *lineGuard {
	return &lineGuard{in: in, r: bufio.NewReaderSize(in, 64<<10), out: out, log: log, limit: sdk.DefaultMaxLineLength}
}

// Read hands the SDK the next message line, whole and ending in a newline.
func (g *lineGuard) Read(p []byte) (int, error) {
	for len(g.buf) == 0 {
		line, err := g.line()
		if line != nil {
			g.buf = line
		}
		if err != nil && len(g.buf) == 0 {
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
		if code, why := check(msg); code != 0 {
			g.reject(code, why)
			return nil, err
		}
		return append(msg, '\n'), err
	}
}

// check reports why a line is not a JSON-RPC message or batch, with
// JSON-RPC's code for it; 0 when it is one.
func check(msg []byte) (int, string) {
	if !json.Valid(msg) {
		return jsonrpc.CodeParseError, "Parse error: the line is not JSON"
	}
	if msg[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(msg, &batch); err != nil || len(batch) == 0 {
			return jsonrpc.CodeInvalidRequest, "Invalid Request: an empty batch"
		}
		for _, m := range batch {
			if _, err := jsonrpc.DecodeMessage(m); err != nil {
				return jsonrpc.CodeInvalidRequest, "Invalid Request: a batch holds something that is not a JSON-RPC 2.0 message"
			}
		}
		return 0, ""
	}
	if _, err := jsonrpc.DecodeMessage(msg); err != nil {
		return jsonrpc.CodeInvalidRequest, "Invalid Request: the line is not a JSON-RPC 2.0 message"
	}
	return 0, ""
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
