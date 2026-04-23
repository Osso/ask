package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// codexProvider implements the Provider interface for OpenAI's codex CLI
// running in `codex app-server` mode. The app-server speaks JSON-RPC 2.0
// over stdio (newline-delimited), much like claude's stream-json. Every
// per-session piece of state lives on the providerProc payload.
//
// MVP scope: plain-text user turns in, agent text out. No images, no
// resume, no model/effort pickers, no native worktree. Richer features
// get layered on top.
type codexProvider struct{}

func init() { registerProvider(codexProvider{}) }

func (codexProvider) ID() string          { return "codex" }
func (codexProvider) DisplayName() string { return "Codex" }

func (codexProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Resume:       true,
		ModelPicker:  true,
		EffortPicker: true,
	}
}

// ModelPicker exposes /model for codex. Real codex model ids (gpt-5,
// o3, …) aren't hardcoded here to avoid guessing — the AllowCustom row
// lets the user type whatever their account supports. A richer picker
// can be layered on once we wire Model/list into the protocol.
func (codexProvider) ModelPicker() ProviderPicker {
	return ProviderPicker{
		Prompt:      "Select Codex model",
		Options:     []string{"default"},
		AllowCustom: true,
	}
}

// EffortOptions matches codex's ReasoningEffort enum ("default" is the
// sentinel used throughout the picker UI meaning "don't set it"; codex
// itself has no "default" value and picks one server-side).
func (codexProvider) EffortOptions() []string {
	return []string{"default", "none", "minimal", "low", "medium", "high", "xhigh"}
}

func (codexProvider) BaseSlashCommands() []slashCmd {
	return []slashCmd{
		{"/resume", "resume a previous Codex session"},
		{"/new", "start a new Codex session"},
		{"/clear", "start a new Codex session"},
		{"/model", "select the Codex model"},
		{"/effort", "select the Codex reasoning effort"},
	}
}

func (codexProvider) ProbeInit(_ ProviderSessionArgs) tea.Cmd { return nil }

func (codexProvider) ListSessions(cwd string) ([]sessionEntry, error) {
	return loadCodexSessions(cwd)
}

func (codexProvider) LoadHistory(sessionID string, opts HistoryOpts) ([]historyEntry, error) {
	return loadCodexHistory(sessionID, opts)
}

func (codexProvider) LoadSettings() ProviderSettings {
	cfg, _ := loadConfig()
	return ProviderSettings{
		Model:         cfg.Codex.Model,
		Effort:        cfg.Codex.Effort,
		SlashCommands: cfg.Codex.SlashCommands,
	}
}

func (codexProvider) SaveSettings(s ProviderSettings) error {
	cfg, _ := loadConfig()
	cfg.Codex.Model = s.Model
	cfg.Codex.Effort = s.Effort
	cfg.Codex.SlashCommands = s.SlashCommands
	return saveConfig(cfg)
}

// codexState is stashed on providerProc.payload. Send reads threadID
// and bumps nextID on every user turn. The stdin mutex serialises
// writes from the Update goroutine (Send) and the stream-reader
// goroutine (approval responders), which would otherwise risk
// interleaving JSON frames on the pipe. done is closed when
// readCodexStream exits so approval responder goroutines can abort
// instead of blocking forever on a dead subprocess.
type codexState struct {
	mu        sync.Mutex
	stdin     io.Writer
	threadID  string
	nextID    uint64
	tabID     int
	effort    string
	skipPerms bool
	done      chan struct{}
}

// codexCLIArgs returns the argv passed to the codex binary. Broken out so
// tests can assert shape without forking a process.
func codexCLIArgs(_ ProviderSessionArgs) []string {
	return []string{"app-server", "--listen", "stdio://"}
}

// handshake request ids — fixed so the handshake scanner can match by id.
const (
	codexInitializeID  = 1
	codexThreadStartID = 2
	// codexTurnStartBaseID is the first id used for user-turn requests; the
	// handshake occupies 1 and 2.
	codexTurnStartBaseID = 3
)

// codexHandshakeTimeout bounds how long StartSession will wait for the
// thread/start response. Observed latency is ~200ms; a 15s ceiling prevents
// a hung codex from locking the tea Update loop forever. The sync handshake
// blocks Update briefly; moving it fully async is deferred (it would require
// a queued-turn path in Send and a ready/error msg type).
const codexHandshakeTimeout = 15 * time.Second

func (codexProvider) StartSession(args ProviderSessionArgs) (*providerProc, chan tea.Msg, error) {
	cmd := exec.Command("codex", codexCLIArgs(args)...)
	if args.Cwd != "" {
		cmd.Dir = args.Cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr := &stderrBuf{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	state := &codexState{
		nextID:    codexTurnStartBaseID,
		stdin:     stdin,
		tabID:     args.TabID,
		effort:    args.Effort,
		skipPerms: args.SkipAllPermissions,
		done:      make(chan struct{}),
	}
	proc := &providerProc{cmd: cmd, stdin: stdin, stderr: stderr, payload: state}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<22)

	// Arm a watchdog that force-kills the subprocess if the handshake hasn't
	// completed in time. Without it, a hung or mis-auth'd codex would block
	// the tea Update loop indefinitely.
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-handshakeDone:
		case <-time.After(codexHandshakeTimeout):
			_ = cmd.Process.Kill()
		}
	}()

	err = codexHandshake(stdin, sc, state, args)
	close(handshakeDone)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("codex handshake: %w (stderr: %s)", err, stderr.String())
	}

	ch := make(chan tea.Msg, 32)
	go readCodexStream(sc, proc, ch)
	return proc, ch, nil
}

// codexHandshake exchanges initialize / initialized / thread/start with the
// app-server and blocks until the thread/start response arrives. It mutates
// state.threadID on success. Frames we see during handshake that aren't the
// thread/start response are discarded — any notifications interleaved here
// (initialize response, mcp startup) have no user-visible effect on a fresh
// thread.
func codexHandshake(stdin io.Writer, sc *bufio.Scanner, state *codexState, args ProviderSessionArgs) error {
	if err := codexWriteJSON(stdin, codexRequest(codexInitializeID, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "ask", "version": "0.1"},
	})); err != nil {
		return err
	}
	if err := codexWriteJSON(stdin, codexNotification("initialized", nil)); err != nil {
		return err
	}
	method := "thread/start"
	params := map[string]any{}
	if args.Cwd != "" {
		params["cwd"] = args.Cwd
	}
	if args.SessionID != "" {
		// Resume: thread/resume takes the thread id directly and
		// returns the same shape as thread/start (thread{id}). Our
		// stored id carries forward; conversation history is
		// replayed from disk by loadCodexHistory earlier in the flow.
		method = "thread/resume"
		params["threadId"] = args.SessionID
	}
	// Skip-all-permissions mirrors claude's --dangerously-skip-permissions:
	// never ask the user, give the agent full access. Applied per-thread
	// here so a subsequent toggle-off + new proc reverts to the default
	// approval flow without any extra plumbing.
	if args.SkipAllPermissions {
		params["approvalPolicy"] = "never"
		params["sandbox"] = "danger-full-access"
	}
	if err := codexWriteJSON(stdin, codexRequest(codexThreadStartID, method, params)); err != nil {
		return err
	}
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		idf, ok := ev["id"].(float64)
		if !ok || int(idf) != codexThreadStartID {
			// Frames we discard here are the initialize response (id=1) and
			// startup notifications (mcpServer/startupStatus/updated,
			// thread/started). None require client action today, but log
			// them so a future auth/capability handshake doesn't fail
			// invisibly.
			debugLog("codex handshake: skipped frame %s", sc.Text())
			continue
		}
		if rpcErr, ok := ev["error"].(map[string]any); ok {
			msg, _ := rpcErr["message"].(string)
			if msg == "" {
				msg = "unknown"
			}
			return fmt.Errorf("thread/start rejected: %s", msg)
		}
		result, _ := ev["result"].(map[string]any)
		thread, _ := result["thread"].(map[string]any)
		tid, _ := thread["id"].(string)
		if tid == "" {
			return fmt.Errorf("thread/start response missing thread.id")
		}
		state.threadID = tid
		debugLog("codex handshake: threadID=%s", tid)
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("codex exited before thread/start response")
}

func (codexProvider) Send(p *providerProc, text string, attachments []pendingAttachment) error {
	state, _ := p.payload.(*codexState)
	if state == nil {
		return fmt.Errorf("codex: session not initialized")
	}
	state.mu.Lock()
	id := state.nextID
	state.nextID++
	tid := state.threadID
	state.mu.Unlock()
	if tid == "" {
		return fmt.Errorf("codex: session not initialized")
	}

	params := map[string]any{
		"threadId": tid,
		"input":    codexUserInput(text, attachments),
	}
	// Effort is set per-turn rather than per-thread because codex's
	// schema only accepts it on TurnStartParams (not ThreadStartParams).
	// "default" (UI sentinel) means "don't override codex's own default".
	if effort := codexEffortForWire(state, p); effort != "" {
		params["effort"] = effort
	}
	return codexWriteJSONLocked(state, codexRequest(id, "turn/start", params))
}

// codexEffortForWire pulls the effort we should send this turn. Right
// now it just reads a per-proc setting we track on the state so every
// turn after a /effort pick lands with the right value; the indirection
// leaves room to read overrides from elsewhere in the future.
func codexEffortForWire(state *codexState, _ *providerProc) string {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.effort == "" || state.effort == "default" {
		return ""
	}
	return state.effort
}

// codexUserInput converts the prompt + any image attachments into
// codex's UserInput array. Text lives in TextUserInput; images land in
// ImageUserInput with a base64 data URL so no temp files are needed
// and codex can inline the bytes directly.
func codexUserInput(text string, attachments []pendingAttachment) []map[string]any {
	out := []map[string]any{{"type": "text", "text": text}}
	for _, a := range attachments {
		if len(a.data) == 0 {
			continue
		}
		mime := a.mime
		if mime == "" {
			mime = "image/png"
		}
		url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(a.data)
		out = append(out, map[string]any{"type": "image", "url": url})
	}
	return out
}

// readCodexStream translates codex's JSON-RPC wire protocol into the
// provider-neutral tea.Msg set. Each event emitted here carries the proc
// pointer so the UI can drop messages from stale subprocesses.
//
// Server-initiated requests (approvals) are dispatched to a separate
// handler that forwards them to the approval modal and writes the
// response back on stdin via the state mutex. Closing state.done on
// exit releases any responder goroutines still waiting on a reply.
func readCodexStream(sc *bufio.Scanner, proc *providerProc, ch chan tea.Msg) {
	defer close(ch)
	if state, ok := proc.payload.(*codexState); ok && state != nil && state.done != nil {
		defer close(state.done)
	}
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if _, _, isRequest := codexServerRequest(ev); isRequest {
			msgs, _ := handleCodexServerRequest(proc, ev)
			for _, msg := range msgs {
				ch <- msg
			}
			continue
		}
		for _, msg := range codexEventToMsgs(ev, proc) {
			ch <- msg
		}
	}
	var err error
	if proc.cmd != nil {
		err = proc.cmd.Wait()
	}
	ch <- providerExitedMsg{err: err, proc: proc}
}

// codexEventToMsgs translates a single codex JSON-RPC frame into zero or
// more tea.Msg values. Pure function so tests exercise it without spawning
// goroutines or subprocesses. Request/response frames (id without method)
// are ignored — we only care about notifications.
func codexEventToMsgs(ev map[string]any, proc *providerProc) []tea.Msg {
	method, _ := ev["method"].(string)
	if method == "" {
		return nil
	}
	switch method {
	case "turn/started":
		return []tea.Msg{streamStatusMsg{status: "thinking…", proc: proc}}
	case "item/started":
		params, _ := ev["params"].(map[string]any)
		item, _ := params["item"].(map[string]any)
		if status := codexItemStatus(item); status != "" {
			return []tea.Msg{streamStatusMsg{status: status, proc: proc}}
		}
		return nil
	case "item/completed":
		params, _ := ev["params"].(map[string]any)
		item, _ := params["item"].(map[string]any)
		if itype, _ := item["type"].(string); itype == "agentMessage" {
			if text, _ := item["text"].(string); text != "" {
				return []tea.Msg{assistantTextMsg{text: text, proc: proc}}
			}
		}
		return nil
	case "turn/completed":
		params, _ := ev["params"].(map[string]any)
		threadID, _ := params["threadId"].(string)
		turn, _ := params["turn"].(map[string]any)
		status, _ := turn["status"].(string)
		res := providerResult{SessionID: threadID}
		if status != "" && status != "completed" {
			res.IsError = true
			if turnErr, _ := turn["error"].(map[string]any); turnErr != nil {
				res.Result, _ = turnErr["message"].(string)
			}
		}
		return []tea.Msg{
			providerDoneMsg{res: res, proc: proc},
			turnCompleteMsg{proc: proc},
		}
	case "error":
		params, _ := ev["params"].(map[string]any)
		errObj, _ := params["error"].(map[string]any)
		msg, _ := errObj["message"].(string)
		if msg == "" {
			msg = "codex reported an unknown error"
		}
		// When willRetry is true, codex is retrying server-side — keep the
		// turn alive so the spinner doesn't flip to idle under the retry.
		// Surface a status line instead.
		willRetry, _ := params["willRetry"].(bool)
		if willRetry {
			return []tea.Msg{streamStatusMsg{status: "retrying: " + truncate(msg, 60), proc: proc}}
		}
		return []tea.Msg{
			providerDoneMsg{res: providerResult{IsError: true, Result: msg}, proc: proc},
			turnCompleteMsg{proc: proc},
		}
	}
	return nil
}

// codexItemStatus maps an item/started payload to a single-line status
// shown next to the spinner. Returns "" when the item type has no useful
// status (e.g. userMessage is our own echo).
func codexItemStatus(item map[string]any) string {
	itype, _ := item["type"].(string)
	switch itype {
	case "reasoning":
		return "reasoning…"
	case "agentMessage":
		return "responding…"
	case "commandExecution":
		if cmd, _ := item["command"].(string); cmd != "" {
			return "shell: " + truncate(cmd, 60)
		}
		return "shell"
	case "fileChange":
		return "editing files…"
	case "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		switch {
		case server != "" && tool != "":
			return "mcp: " + server + "/" + tool
		case tool != "":
			return "mcp: " + tool
		default:
			return "mcp"
		}
	case "plan":
		return "planning…"
	}
	return ""
}

// codexRequest builds a JSON-RPC 2.0 request frame.
func codexRequest(id uint64, method string, params any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
}

// codexNotification builds a JSON-RPC 2.0 notification frame (no id). A nil
// params is elided from the wire frame to match the optional-params shape
// in the codex schema.
func codexNotification(method string, params any) map[string]any {
	m := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		m["params"] = params
	}
	return m
}

// codexWriteJSON marshals msg as a single newline-terminated JSON frame —
// the on-wire format codex app-server uses for stdio:// transport.
// Direct callers must not share the pipe with another goroutine; prefer
// codexWriteJSONLocked when a codexState mutex is available.
func codexWriteJSON(w io.Writer, msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// codexWriteJSONLocked writes a frame under the state mutex so the
// Send path (user turns) and the stream-reader path (approval
// responses) can never interleave bytes on the pipe.
func codexWriteJSONLocked(state *codexState, msg map[string]any) error {
	if state == nil {
		return fmt.Errorf("codex: no state")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stdin == nil {
		return fmt.Errorf("codex: stdin closed")
	}
	return codexWriteJSON(state.stdin, msg)
}
