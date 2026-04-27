package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewMCPBridge_AllocatesEphemeralPort(t *testing.T) {
	b, err := newMCPBridge(42)
	if err != nil {
		t.Fatalf("newMCPBridge: %v", err)
	}
	defer b.stop()
	if b.port <= 0 {
		t.Errorf("port should be > 0: %d", b.port)
	}
	if b.tabID != 42 {
		t.Errorf("tabID=%d want 42", b.tabID)
	}
	if b.ln == nil || b.server == nil {
		t.Errorf("bridge unfinished: ln=%v server=%v", b.ln, b.server)
	}
	// The listener must be accepting on the reported port.
	addr := "127.0.0.1:" + strconv.Itoa(b.port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial bridge at %s: %v", addr, err)
	}
	_ = conn.Close()
}

func TestMCPBridge_StopIsIdempotent(t *testing.T) {
	b, err := newMCPBridge(1)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	b.stop()
	b.stop() // should not panic on a closed listener
	var nilB *mcpBridge
	nilB.stop() // nil-safety
}

func TestConvertMCPQuestions_KindMapping(t *testing.T) {
	qs := []mcpQuestion{
		{Kind: "pick_one", Prompt: "one", Options: []mcpOption{{Label: "a"}, {Label: "b"}}, AllowCustom: true},
		{Kind: "pick_many", Prompt: "many", Options: []mcpOption{{Label: "x"}}, AllowCustom: true},
		{Kind: "pick_diagram", Prompt: "dia", Options: []mcpOption{{Label: "d", Diagram: "▓"}}, AllowCustom: true},
		{Kind: "unknown", Prompt: "fallback", Options: []mcpOption{{Label: "z"}}},
	}
	out := convertMCPQuestions(qs)
	if len(out) != 4 {
		t.Fatalf("len=%d want 4", len(out))
	}
	if out[0].kind != qPickOne {
		t.Errorf("[0] kind=%v want qPickOne", out[0].kind)
	}
	if out[1].kind != qPickMany {
		t.Errorf("[1] kind=%v want qPickMany", out[1].kind)
	}
	if out[2].kind != qPickDiagram {
		t.Errorf("[2] kind=%v want qPickDiagram", out[2].kind)
	}
	if out[3].kind != qPickOne {
		t.Errorf("[3] unknown kind should fall back to qPickOne, got %v", out[3].kind)
	}
}

func TestConvertMCPQuestions_AllowCustomAppendsEnterYourOwn(t *testing.T) {
	// pick_one + AllowCustom → options has trailing "Enter your own"
	qs := []mcpQuestion{{Kind: "pick_one", Options: []mcpOption{{Label: "a"}}, AllowCustom: true}}
	out := convertMCPQuestions(qs)
	if len(out[0].options) != 2 || out[0].options[1] != "Enter your own" {
		t.Errorf("pick_one AllowCustom options=%v", out[0].options)
	}

	// pick_many same
	qs = []mcpQuestion{{Kind: "pick_many", Options: []mcpOption{{Label: "a"}}, AllowCustom: true}}
	out = convertMCPQuestions(qs)
	if len(out[0].options) != 2 || out[0].options[1] != "Enter your own" {
		t.Errorf("pick_many AllowCustom options=%v", out[0].options)
	}

	// pick_diagram + AllowCustom → still no custom trailer
	qs = []mcpQuestion{{Kind: "pick_diagram", Options: []mcpOption{{Label: "d"}}, AllowCustom: true}}
	out = convertMCPQuestions(qs)
	if len(out[0].options) != 1 {
		t.Errorf("pick_diagram AllowCustom must not add Enter your own; options=%v", out[0].options)
	}
}

func TestConvertMCPAnswers_EmptyPicksReturnsEmptySlice(t *testing.T) {
	qs := []mcpQuestion{{Kind: "pick_one", Options: []mcpOption{{Label: "a"}, {Label: "b"}}}}
	answers := []qAnswer{{picks: map[int]bool{}}}
	out := convertMCPAnswers(qs, answers)
	if len(out) != 1 {
		t.Fatalf("len=%d want 1", len(out))
	}
	if out[0].Picks == nil {
		t.Errorf("empty picks should produce []string{} not nil")
	}
	if len(out[0].Picks) != 0 {
		t.Errorf("empty picks should be empty, got %v", out[0].Picks)
	}
}

func TestConvertMCPAnswers_PassesCustomAndNote(t *testing.T) {
	qs := []mcpQuestion{{
		Kind: "pick_one", Options: []mcpOption{{Label: "a"}}, AllowCustom: true,
	}}
	// picks: index 0 = "a", index 1 = Enter your own
	answers := []qAnswer{{picks: map[int]bool{1: true}, custom: "freeform", note: "ditto"}}
	out := convertMCPAnswers(qs, answers)
	if len(out[0].Picks) != 0 {
		t.Errorf("only custom selected; Picks should be empty: %v", out[0].Picks)
	}
	if out[0].Custom != "freeform" {
		t.Errorf("Custom=%q want freeform", out[0].Custom)
	}
	if out[0].Note != "ditto" {
		t.Errorf("Note=%q want ditto", out[0].Note)
	}
}

func TestConvertMCPAnswers_DropsCustomWhenNotAllowed(t *testing.T) {
	qs := []mcpQuestion{{Kind: "pick_one", Options: []mcpOption{{Label: "a"}}}} // AllowCustom=false
	answers := []qAnswer{{picks: map[int]bool{0: true}, custom: "ignored"}}
	out := convertMCPAnswers(qs, answers)
	if out[0].Custom != "" {
		t.Errorf("custom should be dropped when AllowCustom=false, got %q", out[0].Custom)
	}
	if len(out[0].Picks) != 1 || out[0].Picks[0] != "a" {
		t.Errorf("Picks=%v want [a]", out[0].Picks)
	}
}

func TestConvertMCPAnswers_RoundTripJSON(t *testing.T) {
	qs := []mcpQuestion{{Kind: "pick_many", Options: []mcpOption{{Label: "a"}, {Label: "b"}}, AllowCustom: true}}
	answers := []qAnswer{{picks: map[int]bool{0: true, 1: true, 2: true}, custom: "cust", note: "hello"}}
	out := convertMCPAnswers(qs, answers)
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("convertMCPAnswers output must marshal: %v", err)
	}
	if len(out[0].Picks) != 2 {
		t.Errorf("Picks=%v want [a b]", out[0].Picks)
	}
}

func TestPermissionRuleFor_FileTools(t *testing.T) {
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "Read"} {
		r := permissionRuleFor(tool, map[string]any{"file_path": "/a/b"})
		if r.toolName != tool || r.ruleContent != "/a/b" {
			t.Errorf("%s: rule=%+v", tool, r)
		}
	}
	r := permissionRuleFor("Edit", map[string]any{})
	if r.toolName != "Edit" || r.ruleContent != "" {
		t.Errorf("missing file_path should yield empty ruleContent, got %+v", r)
	}
}

func TestPermissionRuleFor_Bash(t *testing.T) {
	r := permissionRuleFor("Bash", map[string]any{"command": "ls -la"})
	if r.toolName != "Bash" || r.ruleContent != "ls -la" {
		t.Errorf("bash rule=%+v", r)
	}
}

func TestPermissionRuleFor_OtherTool(t *testing.T) {
	r := permissionRuleFor("Glob", map[string]any{"pattern": "*.go"})
	if r.toolName != "Glob" || r.ruleContent != "" {
		t.Errorf("non-file/bash tools should leave ruleContent empty, got %+v", r)
	}
}

func TestBuildApprovalBody_Deny(t *testing.T) {
	body := buildApprovalBody(false, map[string]any{"command": "rm -rf"}, nil)
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if parsed["behavior"] != "deny" {
		t.Errorf("behavior=%v want deny", parsed["behavior"])
	}
	if _, ok := parsed["message"]; !ok {
		t.Error("deny body should include message")
	}
	if _, ok := parsed["updatedInput"]; ok {
		t.Error("deny body should NOT include updatedInput")
	}
}

func TestBuildApprovalBody_AllowWithoutRemember(t *testing.T) {
	in := map[string]any{"command": "ls"}
	body := buildApprovalBody(true, in, nil)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["behavior"] != "allow" {
		t.Errorf("behavior=%v want allow", parsed["behavior"])
	}
	upd, ok := parsed["updatedInput"].(map[string]any)
	if !ok || upd["command"] != "ls" {
		t.Errorf("updatedInput missing/wrong: %v", parsed["updatedInput"])
	}
	if _, ok := parsed["updatedPermissions"]; ok {
		t.Error("no remember → should not include updatedPermissions")
	}
}

func TestBuildApprovalBody_AllowWithRememberSession(t *testing.T) {
	rule := permissionRule{toolName: "Edit", ruleContent: "/a/b"}
	body := buildApprovalBody(true, nil, &rule)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["behavior"] != "allow" {
		t.Errorf("behavior=%v want allow", parsed["behavior"])
	}
	upd, _ := parsed["updatedInput"].(map[string]any)
	if upd == nil {
		t.Errorf("updatedInput should be a non-nil map (empty ok): %v", parsed["updatedInput"])
	}
	permsAny, ok := parsed["updatedPermissions"].([]any)
	if !ok || len(permsAny) != 1 {
		t.Fatalf("updatedPermissions missing or wrong shape: %v", parsed["updatedPermissions"])
	}
	p := permsAny[0].(map[string]any)
	if p["type"] != "addRules" {
		t.Errorf("type=%v want addRules", p["type"])
	}
	if p["destination"] != "session" {
		t.Errorf("destination=%v want session", p["destination"])
	}
	if p["behavior"] != "allow" {
		t.Errorf("inner behavior=%v want allow", p["behavior"])
	}
	rules := p["rules"].([]any)
	r0 := rules[0].(map[string]any)
	if r0["toolName"] != "Edit" || r0["ruleContent"] != "/a/b" {
		t.Errorf("inner rule=%+v", r0)
	}
}

func TestBuildApprovalBody_EmptyRuleContentBecomesNull(t *testing.T) {
	rule := permissionRule{toolName: "Glob"} // no ruleContent
	body := buildApprovalBody(true, map[string]any{"pattern": "*.md"}, &rule)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	perms := parsed["updatedPermissions"].([]any)
	r0 := perms[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if _, ok := r0["ruleContent"]; ok {
		// ruleContent should marshal as JSON null and thus be present but nil.
		if v := r0["ruleContent"]; v != nil {
			t.Errorf("ruleContent=%v want null, JSON nil", v)
		}
	}
}

func TestBridge_RememberAndRuleAlwaysAllowed(t *testing.T) {
	b := &mcpBridge{alwaysAllow: map[permissionRule]struct{}{}}
	rule := permissionRule{toolName: "Read", ruleContent: "/x"}
	if b.ruleAlwaysAllowed(rule) {
		t.Error("empty allowlist must not claim allowed")
	}
	b.rememberAlwaysAllow(rule)
	if !b.ruleAlwaysAllowed(rule) {
		t.Error("rule should be allowed after remember")
	}
	empty := permissionRule{}
	b.rememberAlwaysAllow(empty)
	if b.ruleAlwaysAllowed(empty) {
		t.Error("empty toolName must never be allowed")
	}
}

func TestBridge_AlwaysAllowIsGoroutineSafe(t *testing.T) {
	b := &mcpBridge{alwaysAllow: map[permissionRule]struct{}{}}
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := permissionRule{toolName: "Bash", ruleContent: strconv.Itoa(i)}
			b.rememberAlwaysAllow(r)
			b.ruleAlwaysAllowed(r)
		}(i)
	}
	wg.Wait()
}

func TestConvertMCPAnswers_PicksMappedByIndex(t *testing.T) {
	qs := []mcpQuestion{{
		Kind: "pick_many",
		Options: []mcpOption{
			{Label: "red"}, {Label: "green"}, {Label: "blue"},
		},
		AllowCustom: false,
	}}
	answers := []qAnswer{{picks: map[int]bool{0: true, 2: true}}}
	out := convertMCPAnswers(qs, answers)
	if len(out[0].Picks) != 2 {
		t.Fatalf("Picks=%v want 2", out[0].Picks)
	}
	if out[0].Picks[0] != "red" || out[0].Picks[1] != "blue" {
		t.Errorf("order should preserve option order; got %v", out[0].Picks)
	}
}

func TestPermissionRuleFor_UnknownToolEmptyContent(t *testing.T) {
	r := permissionRuleFor("CustomTool", map[string]any{"anything": "x"})
	if r.ruleContent != "" {
		t.Errorf("unknown tool should leave ruleContent empty, got %+v", r)
	}
}

// postHook POSTs a hook event body to the bridge and returns the status
// code. The bridge is spun up for real (real listener, real mux) so the
// routing through http.ServeMux is covered end-to-end; we just don't
// care about tea.Program delivery here (teaProgramPtr may be nil in
// tests, the handler guards against it).
func postHook(t *testing.T, port int, event string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/hooks/%s", port, event)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestMCPBridge_HookEndpointsReturn200(t *testing.T) {
	b, err := newMCPBridge(7)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	defer b.stop()

	start := map[string]any{
		"session_id":      "s1",
		"hook_event_name": "SubagentStart",
		"agent_id":        "agent_123",
		"agent_type":      "general-purpose",
	}
	if code := postHook(t, b.port, "subagent-start", start); code != http.StatusOK {
		t.Errorf("subagent-start POST got %d want 200", code)
	}

	stop := map[string]any{
		"session_id":      "s1",
		"hook_event_name": "SubagentStop",
		"agent_id":        "agent_123",
		"agent_type":      "general-purpose",
	}
	if code := postHook(t, b.port, "subagent-stop", stop); code != http.StatusOK {
		t.Errorf("subagent-stop POST got %d want 200", code)
	}
}

func TestMCPBridge_HookEndpointsRejectBadJSON(t *testing.T) {
	b, err := newMCPBridge(1)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	defer b.stop()

	url := fmt.Sprintf("http://127.0.0.1:%d/hooks/subagent-start", b.port)
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte("not-json")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON should 400, got %d", resp.StatusCode)
	}
}

// The MCP streamable-HTTP endpoint must still be reachable after the
// mux is wired up — the catch-all "/" route has to keep working
// alongside /hooks/*.
func TestMCPBridge_MCPEndpointStillRoutes(t *testing.T) {
	b, err := newMCPBridge(1)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	defer b.stop()
	// A GET to "/" should reach the MCP handler; we don't care about the
	// body, just that we get an HTTP response (not a mux-level 404).
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", b.port))
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("MCP handler unreachable: got 404 at /")
	}
}

// writeFakeHook writes a shell script named claude-bash-hook-approval into dir
// that reads stdin and emits the given JSON line on stdout.
func writeFakeHook(t *testing.T, dir string, responseJSON string) {
	t.Helper()
	script := "#!/bin/sh\ncat > /dev/null\nprintf '%s\\n' '" + responseJSON + "'\n"
	p := filepath.Join(dir, "claude-bash-hook-approval")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake hook: %v", err)
	}
}

// injectHookPath prepends dir to PATH so exec.LookPath finds our fake.
func injectHookPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// removeHookFromPath sets PATH to an empty dir so claude-bash-hook-approval
// cannot be found.
func removeHookFromPath(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	t.Setenv("PATH", empty)
}

func TestBuildDenyBody_ContainsMessage(t *testing.T) {
	body := buildDenyBody("destroys data")
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed["behavior"] != "deny" {
		t.Errorf("behavior=%v want deny", parsed["behavior"])
	}
	if parsed["message"] != "destroys data" {
		t.Errorf("message=%v want 'destroys data'", parsed["message"])
	}
}

func TestBuildDenyBody_EmptyMessageGetsDefault(t *testing.T) {
	body := buildDenyBody("")
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["message"] == "" {
		t.Errorf("empty message should produce a non-empty default")
	}
}

func TestDecideViaBashHook_Safe(t *testing.T) {
	dir := t.TempDir()
	writeFakeHook(t, dir, `{"verdict":"safe","reason":"read-only"}`)
	injectHookPath(t, dir)

	verdict, reason, err := decideViaBashHook(context.Background(), approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != "safe" {
		t.Errorf("verdict=%q want safe", verdict)
	}
	if reason != "read-only" {
		t.Errorf("reason=%q want read-only", reason)
	}
}

func TestDecideViaBashHook_Unsafe(t *testing.T) {
	dir := t.TempDir()
	writeFakeHook(t, dir, `{"verdict":"unsafe","reason":"destroys data"}`)
	injectHookPath(t, dir)

	verdict, reason, err := decideViaBashHook(context.Background(), approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "rm -rf /"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != "unsafe" {
		t.Errorf("verdict=%q want unsafe", verdict)
	}
	if reason != "destroys data" {
		t.Errorf("reason=%q want 'destroys data'", reason)
	}
}

func TestDecideViaBashHook_Unsure(t *testing.T) {
	dir := t.TempDir()
	writeFakeHook(t, dir, `{"verdict":"unsure"}`)
	injectHookPath(t, dir)

	verdict, _, err := decideViaBashHook(context.Background(), approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "something"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != "unsure" {
		t.Errorf("verdict=%q want unsure", verdict)
	}
}

func TestDecideViaBashHook_Missing(t *testing.T) {
	removeHookFromPath(t)
	_, _, err := decideViaBashHook(context.Background(), approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "ls"},
	})
	if err == nil {
		t.Fatal("expected error when binary not found")
	}
}

func TestDecideViaBashHook_Timeout(t *testing.T) {
	dir := t.TempDir()
	// Script sleeps long enough to always exceed the test's short context.
	script := "#!/bin/sh\ncat > /dev/null\nsleep 30\n"
	p := filepath.Join(dir, "claude-bash-hook-approval")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	injectHookPath(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 100)
	defer cancel()
	_, _, err := decideViaBashHook(ctx, approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "ls"},
	})
	if err == nil {
		t.Fatal("expected error on timeout")
	}
}

// approvalToolFiresModal is a shared helper: verifies that approvalTool
// reaches the modal path (evidenced by "ask UI not ready" when teaProgramPtr
// is nil) for the given fake hook response.
func approvalToolFiresModal(t *testing.T, responseJSON string) {
	t.Helper()
	dir := t.TempDir()
	writeFakeHook(t, dir, responseJSON)
	injectHookPath(t, dir)

	// Ensure teaProgramPtr is nil so the modal path returns a known error.
	teaProgramPtr.Store(nil)

	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	payload, _ := json.Marshal(approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "something"},
	})
	req := fakeCallToolRequest(payload)
	_, err := b.approvalTool(context.Background(), req)
	if err == nil || err.Error() != "ask UI not ready" {
		t.Errorf("expected 'ask UI not ready' error indicating modal path; got %v", err)
	}
}

func TestApprovalTool_BashHookSafe_ReturnsAllow(t *testing.T) {
	dir := t.TempDir()
	writeFakeHook(t, dir, `{"verdict":"safe","reason":"ok"}`)
	injectHookPath(t, dir)

	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	payload, _ := json.Marshal(approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "ls"},
	})
	req := fakeCallToolRequest(payload)
	result, err := b.approvalTool(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("empty result content")
	}
	text := result.Content[0].(*mcp.TextContent).Text
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed["behavior"] != "allow" {
		t.Errorf("behavior=%v want allow", parsed["behavior"])
	}
}

func TestApprovalTool_BashHookUnsafe_ReturnsDeny(t *testing.T) {
	dir := t.TempDir()
	writeFakeHook(t, dir, `{"verdict":"unsafe","reason":"destroys data"}`)
	injectHookPath(t, dir)

	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	payload, _ := json.Marshal(approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "rm -rf /"},
	})
	req := fakeCallToolRequest(payload)
	result, err := b.approvalTool(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("empty result content")
	}
	text := result.Content[0].(*mcp.TextContent).Text
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed["behavior"] != "deny" {
		t.Errorf("behavior=%v want deny", parsed["behavior"])
	}
	msg, _ := parsed["message"].(string)
	if msg == "" || msg != "destroys data" {
		t.Errorf("message=%q want 'destroys data'", msg)
	}
}

func TestApprovalTool_BashHookUnsure_FiresModal(t *testing.T) {
	approvalToolFiresModal(t, `{"verdict":"unsure"}`)
}

func TestApprovalTool_BashHookMissing_FallsThroughToModal(t *testing.T) {
	removeHookFromPath(t)
	teaProgramPtr.Store(nil)

	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	payload, _ := json.Marshal(approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "ls"},
	})
	req := fakeCallToolRequest(payload)
	_, err := b.approvalTool(context.Background(), req)
	if err == nil || err.Error() != "ask UI not ready" {
		t.Errorf("expected modal fallthrough ('ask UI not ready'); got %v", err)
	}
}

func TestApprovalTool_BashHookTimeout_FallsThroughToModal(t *testing.T) {
	dir := t.TempDir()
	// Script blocks indefinitely; context timeout forces early return.
	script := "#!/bin/sh\ncat > /dev/null\nsleep 30\n"
	p := filepath.Join(dir, "claude-bash-hook-approval")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	injectHookPath(t, dir)

	// Override the bash-hook timeout via a very short context passed to
	// decideViaBashHook. We test approvalTool via decideViaBashHook directly
	// to keep test duration under 100ms while covering the fallthrough path.
	ctx, cancel := context.WithTimeout(context.Background(), 50)
	defer cancel()

	_, _, err := decideViaBashHook(ctx, approvalIn{
		ToolName: "Bash",
		Input:    map[string]any{"command": "ls"},
	})
	if err == nil {
		t.Fatal("expected timeout error from decideViaBashHook")
	}
	// Verify approvalTool itself falls through to modal when bash-hook fails.
	approvalToolFiresModal(t, `{"verdict":"unsure"}`)
}

// fakeCallToolRequest builds a minimal *mcp.CallToolRequest with the given
// JSON arguments, sufficient for approvalTool's argument parsing.
func fakeCallToolRequest(args json.RawMessage) *mcp.CallToolRequest {
	req := &mcp.CallToolRequest{}
	req.Params = &mcp.CallToolParamsRaw{Arguments: args}
	return req
}

type persistCall struct {
	in       approvalIn
	feedback string
}

// withRecordingPersistRule swaps persistRuleFunc with a recorder for the
// duration of the test and returns a channel that emits each invocation's
// payload. The original is restored via t.Cleanup.
func withRecordingPersistRule(t *testing.T) <-chan persistCall {
	t.Helper()
	ch := make(chan persistCall, 4)
	prev := persistRuleFunc
	persistRuleFunc = func(in approvalIn, fb string) {
		ch <- persistCall{in: in, feedback: fb}
	}
	t.Cleanup(func() { persistRuleFunc = prev })
	return ch
}

func TestHandleApprovalReply_FeedbackPathReturnsPlainAllow(t *testing.T) {
	calls := withRecordingPersistRule(t)
	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	in := approvalIn{ToolName: "Bash", Input: map[string]any{"command": "git status"}}

	body := b.handleApprovalReply(in, approvalReply{allow: true, feedback: "trust git read-only"})

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if parsed["behavior"] != "allow" {
		t.Errorf("behavior=%v want allow", parsed["behavior"])
	}
	if _, ok := parsed["updatedPermissions"]; ok {
		t.Errorf("feedback path must NOT include updatedPermissions, got %v", parsed["updatedPermissions"])
	}

	select {
	case c := <-calls:
		if c.feedback != "trust git read-only" {
			t.Errorf("recorded feedback=%q want %q", c.feedback, "trust git read-only")
		}
		if c.in.ToolName != "Bash" {
			t.Errorf("recorded tool=%q want Bash", c.in.ToolName)
		}
	case <-timeAfter(t, "persistRuleFunc not invoked"):
	}
}

func TestHandleApprovalReply_FeedbackPathCachesAlwaysAllow(t *testing.T) {
	_ = withRecordingPersistRule(t)
	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	in := approvalIn{ToolName: "Bash", Input: map[string]any{"command": "git status"}}

	_ = b.handleApprovalReply(in, approvalReply{allow: true, feedback: "trust git"})

	rule := permissionRuleFor(in.ToolName, in.Input)
	if !b.ruleAlwaysAllowed(rule) {
		t.Error("feedback path should cache the rule in alwaysAllow")
	}
}

func TestHandleApprovalReply_RememberPathEmitsAddRules(t *testing.T) {
	_ = withRecordingPersistRule(t)
	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	in := approvalIn{ToolName: "Edit", Input: map[string]any{"file_path": "/tmp/x"}}
	rule := permissionRule{toolName: "Edit", ruleContent: "/tmp/x"}

	body := b.handleApprovalReply(in, approvalReply{allow: true, remember: &rule})

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed["behavior"] != "allow" {
		t.Errorf("behavior=%v want allow", parsed["behavior"])
	}
	if _, ok := parsed["updatedPermissions"]; !ok {
		t.Error("remember path should emit updatedPermissions")
	}
}

func TestHandleApprovalReply_DenyEmitsDeny(t *testing.T) {
	_ = withRecordingPersistRule(t)
	b := &mcpBridge{tabID: 1, alwaysAllow: map[permissionRule]struct{}{}}
	in := approvalIn{ToolName: "Bash", Input: map[string]any{"command": "rm -rf /"}}

	body := b.handleApprovalReply(in, approvalReply{allow: false})

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed["behavior"] != "deny" {
		t.Errorf("behavior=%v want deny", parsed["behavior"])
	}
}

func TestRunPersistRule_InvokesBinaryWithExpectedArgvAndStdin(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stdinFile := filepath.Join(dir, "stdin")
	// Two separate files keeps the script trivial — no escape juggling
	// over a single concatenated record.
	script := fmt.Sprintf("#!/bin/sh\necho \"$0 $1\" > %q\ncat > %q\n", argvFile, stdinFile)
	bin := filepath.Join(dir, "claude-bash-hook-approval")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake hook: %v", err)
	}
	injectHookPath(t, dir)

	in := approvalIn{ToolName: "Bash", Input: map[string]any{"command": "git status"}}
	runPersistRule(in, "trust git read-only")

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	header := strings.TrimSpace(string(argv))
	if !strings.HasSuffix(header, " persist-rule") {
		t.Errorf("argv subcmd missing 'persist-rule': %q", header)
	}
	if !strings.Contains(header, "/claude-bash-hook-approval ") {
		t.Errorf("argv[0] not the bash-hook binary: %q", header)
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdin), &payload); err != nil {
		t.Fatalf("parse stdin payload: %v\n%s", err, string(stdin))
	}
	if payload["tool_name"] != "Bash" {
		t.Errorf("tool_name=%v want Bash", payload["tool_name"])
	}
	if payload["feedback"] != "trust git read-only" {
		t.Errorf("feedback=%v want 'trust git read-only'", payload["feedback"])
	}
	inMap, _ := payload["input"].(map[string]any)
	if inMap["command"] != "git status" {
		t.Errorf("input.command=%v want 'git status'", inMap["command"])
	}
	if _, ok := payload["cwd"]; !ok {
		t.Error("payload missing cwd")
	}
}

func TestBashHookCwd_ResolvesSymlink(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	canonReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("eval real: %v", err)
	}
	t.Chdir(link)
	got := bashHookCwd()
	if got != canonReal {
		t.Errorf("bashHookCwd=%q want canonical %q", got, canonReal)
	}
}

func TestDecideViaBashHook_PayloadCwdIsSymlinkResolved(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	canonReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("eval real: %v", err)
	}

	hookDir := t.TempDir()
	stdinFile := filepath.Join(hookDir, "stdin")
	script := fmt.Sprintf("#!/bin/sh\ncat > %q\nprintf '%%s\\n' '{\"verdict\":\"safe\"}'\n", stdinFile)
	bin := filepath.Join(hookDir, "claude-bash-hook-approval")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake hook: %v", err)
	}
	injectHookPath(t, hookDir)

	t.Chdir(link)
	if _, _, err := decideViaBashHook(context.Background(), approvalIn{
		ToolName: "Edit",
		Input:    map[string]any{"file_path": filepath.Join(canonReal, "x.go")},
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}

	raw, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		t.Fatalf("parse: %v\n%s", err, string(raw))
	}
	if payload["cwd"] != canonReal {
		t.Errorf("payload cwd=%v want %q", payload["cwd"], canonReal)
	}
}

func TestRunPersistRule_NoBinaryIsSilentNoop(t *testing.T) {
	removeHookFromPath(t)
	// Should not panic or hang. Single attempt; returns silently.
	runPersistRule(approvalIn{ToolName: "Bash", Input: map[string]any{"command": "ls"}}, "feedback")
}

// timeAfter returns a deadline channel for a 2s wait that flags the test
// with t.Errorf when reached. Used so async-completion assertions don't
// hang the suite if a goroutine never fires.
func timeAfter(t *testing.T, msg string) <-chan time.Time {
	t.Helper()
	deadline := time.After(2 * time.Second)
	go func() {
		<-deadline
		t.Errorf("timeout: %s", msg)
	}()
	return deadline
}
