package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexProvider_Metadata(t *testing.T) {
	var p codexProvider
	if got := p.ID(); got != "codex" {
		t.Errorf("ID=%q want codex", got)
	}
	if got := p.DisplayName(); got != "Codex" {
		t.Errorf("DisplayName=%q want Codex", got)
	}
	caps := p.Capabilities()
	// ModelPicker is on (picker accepts a custom model id); everything
	// else is still deferred. Asserted as a set so future flips are loud.
	if !caps.ModelPicker {
		t.Error("ModelPicker capability should be true so /model + Ctrl+B work")
	}
	if !caps.Resume {
		t.Error("Resume capability should be true so /resume works")
	}
	if caps.AskUserQuestionMCP || caps.PermissionPromptMCP {
		t.Errorf("deferred capabilities should stay false, got %+v", caps)
	}
	if !caps.EffortPicker {
		t.Error("EffortPicker capability should be true so /effort works")
	}
	mp := p.ModelPicker()
	if !mp.AllowCustom {
		t.Error("codex ModelPicker must AllowCustom so users can type model ids")
	}
	if len(mp.Options) == 0 {
		t.Error("codex ModelPicker should expose at least a 'default' row")
	}
	eff := p.EffortOptions()
	if len(eff) == 0 {
		t.Error("EffortOptions should expose codex's ReasoningEffort enum")
	}
	wantEffortFirst := "default"
	if len(eff) > 0 && eff[0] != wantEffortFirst {
		t.Errorf("first effort option should be %q (UI sentinel), got %q", wantEffortFirst, eff[0])
	}
	// Slash commands available for codex.
	names := map[string]bool{}
	for _, s := range p.BaseSlashCommands() {
		names[s.name] = true
	}
	for _, want := range []string{"/resume", "/new", "/clear", "/model", "/effort"} {
		if !names[want] {
			t.Errorf("BaseSlashCommands missing %q (got %v)", want, names)
		}
	}
}

func TestCodexProvider_RegistersItself(t *testing.T) {
	if p := providerByID("codex"); p == nil {
		t.Fatal("providerByID(\"codex\") returned nil — codex provider not registered in init()")
	} else if p.ID() != "codex" {
		t.Errorf("providerByID(\"codex\") returned %q", p.ID())
	}
}

func TestCodexProvider_ClaudeRemainsDefault(t *testing.T) {
	// Adding codex to the registry must not flip the no-id fallback; claude
	// is still the default for existing users.
	if len(providerRegistry) == 0 {
		t.Skip("no providers registered")
	}
	first := providerRegistry[0]
	if first.ID() != "claude" {
		t.Fatalf("first registered provider = %q, want claude (init order regression)", first.ID())
	}
}

func TestCodexSend_WritesTurnStartRequest(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{threadID: "t-1", nextID: codexTurnStartBaseID, stdin: buf}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	if err := cp.Send(p, "hello world", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Errorf("wire frame must end with newline; got %q", buf.String())
	}
	var env map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env); err != nil {
		t.Fatalf("invalid JSON %q: %v", buf.String(), err)
	}
	if env["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc=%v want 2.0", env["jsonrpc"])
	}
	if env["method"] != "turn/start" {
		t.Errorf("method=%v want turn/start", env["method"])
	}
	if idf, ok := env["id"].(float64); !ok || uint64(idf) != codexTurnStartBaseID {
		t.Errorf("id=%v want %d", env["id"], codexTurnStartBaseID)
	}
	params, ok := env["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing: %v", env)
	}
	if params["threadId"] != "t-1" {
		t.Errorf("threadId=%v want t-1", params["threadId"])
	}
	input, ok := params["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input shape wrong: %v", params["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "hello world" {
		t.Errorf("input[0]=%v want {type:text, text:hello world}", first)
	}
}

func TestCodexSend_IncrementsIDAcrossTurns(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{threadID: "t-1", nextID: codexTurnStartBaseID, stdin: buf}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	for i := 0; i < 3; i++ {
		buf.Reset()
		if err := cp.Send(p, "turn", nil); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		var env map[string]any
		_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
		idf, _ := env["id"].(float64)
		want := uint64(codexTurnStartBaseID + i)
		if uint64(idf) != want {
			t.Errorf("turn #%d id=%v want %d", i, idf, want)
		}
	}
}

func TestCodexSend_ErrorsWhenUninitialized(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	// No payload set — simulates a Send call against a half-forked proc.
	p := &providerProc{stdin: buf}
	var cp codexProvider
	if err := cp.Send(p, "hi", nil); err == nil {
		t.Fatal("Send without codexState payload must error")
	}
	if buf.Len() != 0 {
		t.Errorf("no bytes should be written when Send errors, got %q", buf.String())
	}
}

func TestCodexSend_ErrorsWhenThreadIDEmpty(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	p := &providerProc{stdin: buf, payload: &codexState{}}
	var cp codexProvider
	if err := cp.Send(p, "hi", nil); err == nil {
		t.Fatal("Send with empty threadID must error")
	}
}

func TestCodexSend_ImageAttachmentsEmitDataURL(t *testing.T) {
	// Attachments now flow through as codex ImageUserInput items with
	// a base64 data URL. Text stays as the first entry so the turn
	// reads correctly.
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{threadID: "t-1", nextID: codexTurnStartBaseID, stdin: buf}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	raw := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	err := cp.Send(p, "look", []pendingAttachment{{data: raw, mime: "image/png"}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var env map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	params := env["params"].(map[string]any)
	input := params["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("want 2 input items (text+image), got %d: %v", len(input), input)
	}
	first, _ := input[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "look" {
		t.Errorf("input[0]=%v want text 'look'", first)
	}
	second, _ := input[1].(map[string]any)
	if second["type"] != "image" {
		t.Errorf("input[1].type=%v want image", second["type"])
	}
	url, _ := second["url"].(string)
	wantPrefix := "data:image/png;base64,"
	if !strings.HasPrefix(url, wantPrefix) {
		t.Fatalf("image url missing data-URL prefix, got %q", url)
	}
	b64 := strings.TrimPrefix(url, wantPrefix)
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("image url payload not valid base64: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Errorf("image payload round-trip mismatch: got %v want %v", decoded, raw)
	}
}

func TestCodexSend_EmptyAttachmentDataSkipped(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{threadID: "t-1", nextID: codexTurnStartBaseID, stdin: buf}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	err := cp.Send(p, "hi", []pendingAttachment{{mime: "image/png"}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var env map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	params := env["params"].(map[string]any)
	input := params["input"].([]any)
	if len(input) != 1 {
		t.Errorf("empty attachment data should be skipped, got %v", input)
	}
}

func TestCodexSend_PassesEffortWhenSet(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{
		threadID: "t-1",
		nextID:   codexTurnStartBaseID,
		stdin:    buf,
		effort:   "high",
	}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	if err := cp.Send(p, "hi", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var env map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	params := env["params"].(map[string]any)
	if params["effort"] != "high" {
		t.Errorf("turn/start.effort=%v want high", params["effort"])
	}
}

func TestCodexSend_OmitsEffortForDefault(t *testing.T) {
	// The UI's "default" sentinel means "don't set effort" — codex
	// will pick its own default when the field is absent.
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{
		threadID: "t-1",
		nextID:   codexTurnStartBaseID,
		stdin:    buf,
		effort:   "default",
	}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	if err := cp.Send(p, "hi", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var env map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env)
	params := env["params"].(map[string]any)
	if _, present := params["effort"]; present {
		t.Errorf("effort='default' must omit the field; got %v", params["effort"])
	}
}

func TestCodexRequest_Shape(t *testing.T) {
	r := codexRequest(42, "turn/start", map[string]any{"threadId": "t"})
	if r["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc=%v", r["jsonrpc"])
	}
	if id, _ := r["id"].(uint64); id != 42 {
		t.Errorf("id=%v", r["id"])
	}
	if r["method"] != "turn/start" {
		t.Errorf("method=%v", r["method"])
	}
	if _, ok := r["params"].(map[string]any); !ok {
		t.Errorf("params wrong type: %T", r["params"])
	}
}

func TestCodexNotification_OmitsNilParams(t *testing.T) {
	n := codexNotification("initialized", nil)
	if _, present := n["params"]; present {
		t.Errorf("nil params must be elided from wire frame; got %v", n)
	}
	if _, present := n["id"]; present {
		t.Errorf("notification must not carry id; got %v", n)
	}
	if n["jsonrpc"] != "2.0" || n["method"] != "initialized" {
		t.Errorf("envelope fields wrong: %v", n)
	}
}

func TestCodexNotification_IncludesParamsWhenPresent(t *testing.T) {
	n := codexNotification("thing", map[string]any{"k": "v"})
	params, ok := n["params"].(map[string]any)
	if !ok {
		t.Fatalf("params wrong: %T", n["params"])
	}
	if params["k"] != "v" {
		t.Errorf("params body lost: %v", params)
	}
}

func TestCodexWriteJSON_EmitsNewlineDelimited(t *testing.T) {
	var buf bytes.Buffer
	if err := codexWriteJSON(&buf, map[string]any{"method": "x"}); err != nil {
		t.Fatalf("codexWriteJSON: %v", err)
	}
	if err := codexWriteJSON(&buf, map[string]any{"method": "y"}); err != nil {
		t.Fatalf("codexWriteJSON: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("want 2 frames, got %d: %q", len(lines), buf.String())
	}
	for _, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal(ln, &m); err != nil {
			t.Errorf("frame not valid JSON: %q", ln)
		}
	}
}

func TestCodexUserInput_TextOnly(t *testing.T) {
	items := codexUserInput("hello", nil)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0]["type"] != "text" || items[0]["text"] != "hello" {
		t.Errorf("item wrong: %v", items[0])
	}
}

func TestCodexUserInput_EmptyTextStillProducesItem(t *testing.T) {
	// Even with an empty prompt the UserInput array must contain one text
	// item — codex's TurnStartParams requires a non-empty input array.
	items := codexUserInput("", nil)
	if len(items) != 1 || items[0]["type"] != "text" {
		t.Fatalf("empty input should still produce a single text item, got %v", items)
	}
}

func TestCodexConfig_RoundTrip(t *testing.T) {
	// LoadSettings / SaveSettings should scope reads/writes to the Codex
	// config section — writing codex settings must not clobber Claude's.
	isolateHome(t)
	// Seed a claude config we expect to survive.
	pre := askConfig{
		Claude: claudeConfig{Model: "sonnet", Effort: "high"},
	}
	if err := saveConfig(pre); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	var cp codexProvider
	if err := cp.SaveSettings(ProviderSettings{Model: "gpt-5", Effort: "low"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got := cp.LoadSettings()
	if got.Model != "gpt-5" || got.Effort != "low" {
		t.Errorf("codex settings did not round-trip: %+v", got)
	}

	// Claude side must be untouched.
	cfg, _ := loadConfig()
	if cfg.Claude.Model != "sonnet" || cfg.Claude.Effort != "high" {
		t.Errorf("saving codex settings clobbered claude config: %+v", cfg.Claude)
	}
}
