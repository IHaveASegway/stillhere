package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Every error says what to pass instead (docs/spec/mcp.md, "Errors"),
// including an argument the tool's input schema refuses (MC-9). The SDK
// checks the arguments against the schema before a handler runs and
// answers a miss with its validator's own words ("validating /properties/
// limit: maximum: 1000/1 is greater than 100.000000"), which say what is
// wrong but not what to pass. A receiving middleware runs this check first,
// from the same schema the tool publishes, so a refusal names the argument,
// what it takes and, where it helps, how to get the rest; the SDK's check
// stays behind it. The schemas use a few keywords only, and parsing one
// with a keyword the check does not enforce fails when the server is built,
// so the two checks cannot drift apart unnoticed.

// argSchema is a tool's input schema as schema writes it: the keywords the
// check enforces, and no other.
type argSchema struct {
	Type                 string             `json:"type"`
	Properties           map[string]argProp `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties bool               `json:"additionalProperties"`
}

// argProp is one argument's schema.
type argProp struct {
	Type        string   `json:"type"` // string or integer
	Enum        []string `json:"enum"`
	Minimum     *float64 `json:"minimum"`
	Maximum     *float64 `json:"maximum"`
	Description string   `json:"description"`
}

// argHints say, for an argument over its maximum, how to get what the agent
// asked for another way.
var argHints = map[string]string{
	"limit":     "pass limit: 100, the most a page holds, and follow page.next_cursor for the rest",
	"max_chars": "pass path to see one part of a large change",
}

// parseArgSchema reads a tool's input schema; a keyword it does not know
// is a bug in the tool's definition, so it panics, as the SDK's AddTool
// does on a schema it cannot use.
func parseArgSchema(tool string, raw json.RawMessage) argSchema {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var a argSchema
	if err := dec.Decode(&a); err != nil {
		panic(fmt.Sprintf("mcp: %s's input schema uses what the argument check does not enforce: %v", tool, err))
	}
	for name, p := range a.Properties {
		if p.Type != "string" && p.Type != "integer" {
			panic(fmt.Sprintf("mcp: %s's argument %s is of type %q, which the argument check does not enforce", tool, name, p.Type))
		}
	}
	return a
}

// checkArgs is the receiving middleware that checks a tool call's
// arguments against its tool's schema before the SDK does, and refuses a
// miss with what to pass.
func (s *server) checkArgs(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		if r, ok := req.(*sdk.CallToolRequest); ok && method == "tools/call" && r.Params != nil {
			if a, ok := s.schemas[r.Params.Name]; ok {
				if msg := a.check(r.Params.Name, r.Params.Arguments); msg != "" {
					s.log.Info("tool refused its arguments", "tool", r.Params.Name)
					return s.toolError(&domain.UsageError{Msg: msg}, nil), nil
				}
			}
		}
		return next(ctx, method, req)
	}
}

// check returns why the arguments do not fit the schema, saying what to
// pass, or "" when they fit.
func (a argSchema) check(tool string, raw json.RawMessage) string {
	args := map[string]json.RawMessage{}
	if b := bytes.TrimSpace(raw); len(b) > 0 && string(b) != "null" {
		if err := json.Unmarshal(b, &args); err != nil {
			return fmt.Sprintf("%s takes its arguments as one JSON object: %s.", tool, a.names())
		}
	}
	for _, name := range slices.Sorted(maps.Keys(args)) {
		p, ok := a.Properties[name]
		if !ok {
			return fmt.Sprintf("%s has no argument %q: it takes %s.", tool, name, a.names())
		}
		if why, over := p.check(args[name]); why != "" {
			msg := fmt.Sprintf("%s: %s %s", tool, name, strings.TrimSuffix(why, "."))
			if hint := argHints[name]; hint != "" && over {
				msg += "; " + hint
			}
			return msg + "."
		}
	}
	for _, name := range a.Required {
		if _, ok := args[name]; !ok {
			return fmt.Sprintf("%s needs %s: %s", tool, name, a.Properties[name].Description)
		}
	}
	return ""
}

// names lists the arguments a tool takes, the required ones marked.
func (a argSchema) names() string {
	if len(a.Properties) == 0 {
		return "no arguments"
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(a.Properties)) {
		if slices.Contains(a.Required, name) {
			name += " (required)"
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

// check returns why one argument's value does not fit, or "", and whether
// it is a number over the argument's maximum.
func (p argProp) check(raw json.RawMessage) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "is not JSON", false
	}
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return "is a string: " + p.Description, false
		}
		if len(p.Enum) > 0 && !slices.Contains(p.Enum, s) {
			return fmt.Sprintf("%q is not one of %s", s, strings.Join(p.Enum, ", ")), false
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) {
			return "is a whole number" + p.bounds() + ", not " + string(raw), false
		}
		over := p.Maximum != nil && f > *p.Maximum
		if over || (p.Minimum != nil && f < *p.Minimum) {
			return "is a whole number" + p.bounds() + ", not " + strconv.FormatFloat(f, 'f', -1, 64), over
		}
	}
	return "", false
}

// bounds says an integer argument's range.
func (p argProp) bounds() string {
	format := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	switch {
	case p.Minimum != nil && p.Maximum != nil:
		return " from " + format(*p.Minimum) + " to " + format(*p.Maximum)
	case p.Minimum != nil:
		return " of at least " + format(*p.Minimum)
	case p.Maximum != nil:
		return " of at most " + format(*p.Maximum)
	}
	return ""
}
