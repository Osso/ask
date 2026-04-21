package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const askProgressInterval = 20 * time.Second

type mcpOption struct {
	Label   string `json:"label" jsonschema:"short label for the option"`
	Diagram string `json:"diagram,omitempty" jsonschema:"required only for pick_diagram kind: monospace box-drawing art, max 40 cols x 12 rows"`
}

type mcpQuestion struct {
	Kind        string      `json:"kind" jsonschema:"one of pick_one, pick_many, pick_diagram"`
	Prompt      string      `json:"prompt" jsonschema:"the question shown to the user"`
	Options     []mcpOption `json:"options" jsonschema:"list of options for the user to choose from"`
	AllowCustom bool        `json:"allow_custom,omitempty" jsonschema:"append an Enter-your-own free-text option (pick_one and pick_many only)"`
}

type askInput struct {
	Questions []mcpQuestion `json:"questions" jsonschema:"one or more questions to ask the user together in a tabbed modal"`
}

type mcpAnswer struct {
	Picks  []string `json:"picks" jsonschema:"labels of options the user selected; empty if user only entered a custom answer"`
	Custom string   `json:"custom,omitempty" jsonschema:"free-form text if the user used Enter your own"`
	Note   string   `json:"note,omitempty" jsonschema:"additional note the user attached via n"`
}

type askOutput struct {
	Answers   []mcpAnswer `json:"answers" jsonschema:"answers in the same order as input questions"`
	Cancelled bool        `json:"cancelled,omitempty" jsonschema:"true if the user dismissed the dialog without submitting"`
}

type askReply struct {
	answers   []qAnswer
	cancelled bool
}

type askToolRequestMsg struct {
	questions []question
	reply     chan askReply
}

type approvalIn struct {
	ToolName  string         `json:"tool_name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
}

type approvalReply struct {
	allow bool
}

type approvalRequestMsg struct {
	toolName  string
	input     map[string]any
	toolUseID string
	reply     chan approvalReply
}

const approvalToolDescription = `Permission prompt callback wired up via claude's --permission-prompt-tool.
Do not call this tool directly. It is invoked by claude before running a tool so
the ask TUI can show an approval modal to the user; the user answers allow or
deny and the decision is returned as a stringified JSON body.`

// approvalInputSchema is emitted verbatim in tools/list. The low-level
// Server.AddTool path doesn't auto-generate a schema, so we supply one by hand.
var approvalInputSchema = json.RawMessage(`{"type":"object","properties":{"tool_name":{"type":"string"},"input":{"type":"object"},"tool_use_id":{"type":"string"}},"required":["tool_name","input"]}`)

const askToolDescription = `Ask the user one or more questions through a tabbed modal in the ask terminal UI.

Each question is one of three kinds:
  - "pick_one": user picks exactly one option
  - "pick_many": user picks zero or more options
  - "pick_diagram": user picks exactly one option; each option has an ASCII-art
    preview that is rendered in a side box as the user navigates the list

All submitted questions are displayed together as tabs; the user answers each
before submitting. Answers are returned in input order.

Diagram format (pick_diagram only; strict):
  - Monospace box-drawing characters only: ╭╮╰╯─│├┤┬┴┼
  - Fill blocks: ░ for content areas, ▓ for interactive or accent areas
  - No emoji, no tabs, no trailing whitespace
  - At most 40 columns wide and 12 rows tall; all diagrams in one question are
    padded to the same bounding box before rendering, so smaller is fine

Set allow_custom=true on pick_one or pick_many to append an Enter-your-own
option that accepts free-form multi-line text from the user.`

type mcpBridge struct {
	program atomic.Pointer[tea.Program]
	port    int
	ln      net.Listener
	server  *mcp.Server
}

func newMCPBridge() (*mcpBridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &mcpBridge{
		ln:   ln,
		port: ln.Addr().(*net.TCPAddr).Port,
	}
	b.server = mcp.NewServer(&mcp.Implementation{Name: "ask", Version: "0.1"}, nil)
	mcp.AddTool(b.server, &mcp.Tool{
		Name:        "ask_user_question",
		Description: askToolDescription,
	}, b.askTool)
	b.server.AddTool(&mcp.Tool{
		Name:        "approval_prompt",
		Description: approvalToolDescription,
		InputSchema: approvalInputSchema,
	}, b.approvalTool)
	return b, nil
}

func (b *mcpBridge) start(p *tea.Program) {
	b.program.Store(p)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return b.server },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true, Stateless: true},
	)
	go func() {
		_ = http.Serve(b.ln, handler)
	}()
}

func (b *mcpBridge) askTool(ctx context.Context, req *mcp.CallToolRequest, in askInput) (*mcp.CallToolResult, askOutput, error) {
	if len(in.Questions) == 0 {
		return nil, askOutput{}, errors.New("at least one question is required")
	}
	p := b.program.Load()
	if p == nil {
		return nil, askOutput{}, errors.New("ask UI not ready")
	}
	reply := make(chan askReply, 1)
	p.Send(askToolRequestMsg{
		questions: convertMCPQuestions(in.Questions),
		reply:     reply,
	})
	if token := req.Params.GetProgressToken(); token != nil && req.Session != nil {
		done := make(chan struct{})
		defer close(done)
		go b.pingProgress(ctx, req.Session, token, done)
	}
	select {
	case resp := <-reply:
		if resp.cancelled {
			out := askOutput{Cancelled: true}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "user cancelled the dialog"}},
				IsError: true,
			}, out, nil
		}
		out := askOutput{Answers: convertMCPAnswers(in.Questions, resp.answers)}
		body, err := json.Marshal(out)
		if err != nil {
			return nil, askOutput{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
		}, out, nil
	case <-ctx.Done():
		return nil, askOutput{}, ctx.Err()
	}
}

// approvalTool is registered via the low-level Server.AddTool. That path skips
// auto-populating CallToolResult.StructuredContent and auto-running our output
// through jsonschema defaults/validation, so the wire response is exactly the
// stringified-JSON text block claude's --permission-prompt-tool expects.
func (b *mcpBridge) approvalTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in approvalIn
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, fmt.Errorf("approval_prompt: %w", err)
		}
	}
	p := b.program.Load()
	if p == nil {
		return nil, errors.New("ask UI not ready")
	}
	reply := make(chan approvalReply, 1)
	p.Send(approvalRequestMsg{
		toolName:  in.ToolName,
		input:     in.Input,
		toolUseID: in.ToolUseID,
		reply:     reply,
	})
	if token := req.Params.GetProgressToken(); token != nil && req.Session != nil {
		done := make(chan struct{})
		defer close(done)
		go b.pingProgress(ctx, req.Session, token, done)
	}
	select {
	case resp := <-reply:
		var decision map[string]any
		if resp.allow {
			updated := in.Input
			if updated == nil {
				updated = map[string]any{}
			}
			decision = map[string]any{"behavior": "allow", "updatedInput": updated}
		} else {
			decision = map[string]any{"behavior": "deny", "message": "user denied the approval"}
		}
		body, err := json.Marshal(decision)
		if err != nil {
			return nil, err
		}
		debugLog("approval_prompt reply tool=%s allow=%v body=%s", in.ToolName, resp.allow, string(body))
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *mcpBridge) pingProgress(ctx context.Context, sess *mcp.ServerSession, token any, done <-chan struct{}) {
	ticker := time.NewTicker(askProgressInterval)
	defer ticker.Stop()
	var n float64
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			n++
			_ = sess.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      n,
				Message:       "waiting for user",
			})
		}
	}
}

func convertMCPQuestions(qs []mcpQuestion) []question {
	out := make([]question, len(qs))
	for i, q := range qs {
		var kind qKind
		switch q.Kind {
		case "pick_many":
			kind = qPickMany
		case "pick_diagram":
			kind = qPickDiagram
		default:
			kind = qPickOne
		}
		labels := make([]string, 0, len(q.Options)+1)
		diagrams := make([]string, 0, len(q.Options)+1)
		for _, o := range q.Options {
			labels = append(labels, o.Label)
			diagrams = append(diagrams, o.Diagram)
		}
		if q.AllowCustom && kind != qPickDiagram {
			labels = append(labels, "Enter your own")
			diagrams = append(diagrams, "")
		}
		out[i] = question{
			kind:     kind,
			prompt:   q.Prompt,
			options:  labels,
			diagrams: diagrams,
		}
	}
	return out
}

func convertMCPAnswers(qs []mcpQuestion, answers []qAnswer) []mcpAnswer {
	out := make([]mcpAnswer, len(qs))
	for i, q := range qs {
		ans := answers[i]
		customIdx := -1
		if q.AllowCustom && q.Kind != "pick_diagram" {
			customIdx = len(q.Options)
		}
		var picks []string
		for idx := 0; idx < len(q.Options); idx++ {
			if ans.picks[idx] {
				picks = append(picks, q.Options[idx].Label)
			}
		}
		if picks == nil {
			picks = []string{}
		}
		custom := ""
		if customIdx >= 0 && ans.picks[customIdx] {
			custom = ans.custom
		}
		out[i] = mcpAnswer{
			Picks:  picks,
			Custom: custom,
			Note:   ans.note,
		}
	}
	return out
}
