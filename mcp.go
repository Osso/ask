package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

func (b *mcpBridge) askTool(ctx context.Context, _ *mcp.CallToolRequest, in askInput) (*mcp.CallToolResult, askOutput, error) {
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
