package main

import (
	"strings"
	"testing"
)

func TestRenderToolCallBlock_IncludesNameAndInputs(t *testing.T) {
	out := renderToolCallBlock("Read", map[string]any{"file_path": "/x.go"}, toolOutputFull)
	if !strings.Contains(out, "Read") {
		t.Errorf("block missing tool name: %q", out)
	}
	if !strings.Contains(out, "file_path") || !strings.Contains(out, "/x.go") {
		t.Errorf("block missing input field: %q", out)
	}
}

func TestRenderToolCallBlock_EmptyNameFallsBack(t *testing.T) {
	out := renderToolCallBlock("", nil, toolOutputFull)
	if !strings.Contains(out, "tool") {
		t.Errorf("empty name should fall back to 'tool'; got %q", out)
	}
}

func TestRenderToolCallBlock_SortedKeys(t *testing.T) {
	// Maps randomize iteration; the renderer sorts keys so successive
	// renders of the same payload stay byte-identical.
	input := map[string]any{"zeta": 1, "alpha": 2, "mu": 3}
	a := renderToolCallBlock("X", input, toolOutputFull)
	b := renderToolCallBlock("X", input, toolOutputFull)
	if a != b {
		t.Errorf("renderer must be deterministic across calls")
	}
	// alpha should appear before mu should appear before zeta.
	ai := strings.Index(a, "alpha:")
	mi := strings.Index(a, "mu:")
	zi := strings.Index(a, "zeta:")
	if ai < 0 || mi < 0 || zi < 0 || !(ai < mi && mi < zi) {
		t.Errorf("keys not in sorted order: %q", a)
	}
}

func TestRenderToolCallBlock_ShortFiltersToAllowlist(t *testing.T) {
	// Bash in short mode shows only command, hides other inputs the user
	// asked the renderer to elide.
	input := map[string]any{
		"command":           "ls /tmp",
		"description":       "list tmp",
		"run_in_background": true,
	}
	out := renderToolCallBlock("Bash", input, toolOutputShort)
	if !strings.Contains(out, "command") || !strings.Contains(out, "ls /tmp") {
		t.Errorf("short Bash should keep command; got %q", out)
	}
	if strings.Contains(out, "description") || strings.Contains(out, "run_in_background") {
		t.Errorf("short Bash should drop non-allowlisted inputs; got %q", out)
	}
}

func TestRenderToolCallBlock_ShortUnknownToolHeaderOnly(t *testing.T) {
	// Tools without an allowlist entry render as just the header in
	// short mode — users still see something fired but no payload spam.
	out := renderToolCallBlock("MysteryMCP", map[string]any{"foo": "bar", "baz": 42}, toolOutputShort)
	if !strings.Contains(out, "MysteryMCP") {
		t.Errorf("header missing: %q", out)
	}
	if strings.Contains(out, "foo") || strings.Contains(out, "baz") {
		t.Errorf("short mode should drop unknown-tool inputs; got %q", out)
	}
}

func TestNextToolOutputMode_Cycles(t *testing.T) {
	// /config row cycles full → short → off → full. Unknown values reset
	// to the default so the picker can never wedge.
	if got := nextToolOutputMode(toolOutputFull); got != toolOutputShort {
		t.Errorf("full → %q want %q", got, toolOutputShort)
	}
	if got := nextToolOutputMode(toolOutputShort); got != toolOutputOff {
		t.Errorf("short → %q want %q", got, toolOutputOff)
	}
	if got := nextToolOutputMode(toolOutputOff); got != toolOutputFull {
		t.Errorf("off → %q want %q", got, toolOutputFull)
	}
	if got := nextToolOutputMode("garbage"); got != defaultToolOutputMode {
		t.Errorf("garbage → %q want %q", got, defaultToolOutputMode)
	}
}

func TestParseToolOutputMode_Defaults(t *testing.T) {
	// Empty and unrecognized values fall through to the default so a typo
	// in ask.json never silences tool output entirely.
	if got := parseToolOutputMode(""); got != defaultToolOutputMode {
		t.Errorf("empty should default; got %q", got)
	}
	if got := parseToolOutputMode("loud"); got != defaultToolOutputMode {
		t.Errorf("unknown should default; got %q", got)
	}
	for _, v := range []toolOutputMode{toolOutputFull, toolOutputShort, toolOutputOff} {
		if got := parseToolOutputMode(string(v)); got != v {
			t.Errorf("known %q lost: got %q", v, got)
		}
	}
}

func TestRenderToolCallActions_SingleUnknownUsesProgramAsHeader(t *testing.T) {
	// A single Unknown action collapses into the header itself so a
	// `git log -6` call shows as "▸ git log -6", not as a "shell" header
	// with a noisy /usr/bin/zsh -lc wrapper underneath.
	actions := []map[string]any{
		{"type": "unknown", "command": "git log --oneline -6"},
	}
	out := renderToolCallActionsBlock("shell", actions, toolOutputShort)
	if !strings.Contains(out, "git log --oneline -6") {
		t.Errorf("expected unwrapped command in header; got %q", out)
	}
	if strings.Contains(out, "shell") {
		t.Errorf("single-action call should not show shell fallback header; got %q", out)
	}
}

func TestRenderToolCallActions_SingleReadShowsName(t *testing.T) {
	actions := []map[string]any{
		{"type": "read", "command": "cat main.go", "name": "main.go", "path": "/x/main.go"},
	}
	out := renderToolCallActionsBlock("shell", actions, toolOutputShort)
	if !strings.Contains(out, "read main.go") {
		t.Errorf("expected 'read main.go' header; got %q", out)
	}
}

func TestRenderToolCallActions_GroupsConsecutiveReads(t *testing.T) {
	// Multiple reads in one call collapse to a single comma-separated row,
	// matching Codex's exec_cell rendering. Header reverts to "shell"
	// because the rendered row count is 1 but the body is a join.
	actions := []map[string]any{
		{"type": "read", "name": "a.go"},
		{"type": "read", "name": "b.go"},
		{"type": "read", "name": "a.go"}, // duplicate dropped
	}
	out := renderToolCallActionsBlock("shell", actions, toolOutputShort)
	if !strings.Contains(out, "read a.go, b.go") {
		t.Errorf("expected grouped reads with dedup; got %q", out)
	}
	if strings.Count(out, "read") != 1 {
		t.Errorf("expected exactly one 'read' row after grouping; got %q", out)
	}
}

func TestRenderToolCallActions_MixedActionsListsRows(t *testing.T) {
	// Mixed actions get the generic shell header and one indented row per
	// action. Search renders "<query> in <path>"; unknown uses the program
	// token as title.
	actions := []map[string]any{
		{"type": "read", "name": "main.go"},
		{"type": "search", "query": "TODO", "path": "src/", "command": "rg TODO src/"},
		{"type": "unknown", "command": "git status"},
	}
	out := renderToolCallActionsBlock("shell", actions, toolOutputShort)
	if !strings.Contains(out, "▸") || !strings.Contains(out, "shell") {
		t.Errorf("expected shell header for mixed actions; got %q", out)
	}
	for _, want := range []string{"read main.go", "search TODO in src/", "git status"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in mixed render: %q", want, out)
		}
	}
}

func TestRenderToolCallActions_SearchFallsBackToCommand(t *testing.T) {
	// When a Search action has neither query nor path (Codex couldn't
	// disambiguate), we render the raw command so the user still sees
	// what ran instead of an empty row.
	actions := []map[string]any{
		{"type": "search", "command": "grep -r foo ."},
	}
	out := renderToolCallActionsBlock("shell", actions, toolOutputShort)
	if !strings.Contains(out, "search grep -r foo .") {
		t.Errorf("expected fallback to command; got %q", out)
	}
}

func TestRenderToolCallActions_ListFilesUsesPathOrCommand(t *testing.T) {
	withPath := []map[string]any{
		{"type": "listFiles", "path": "src/", "command": "ls src/"},
	}
	if out := renderToolCallActionsBlock("shell", withPath, toolOutputShort); !strings.Contains(out, "list src/") {
		t.Errorf("listFiles with path should use path; got %q", out)
	}
	noPath := []map[string]any{
		{"type": "listFiles", "command": "ls -la"},
	}
	if out := renderToolCallActionsBlock("shell", noPath, toolOutputShort); !strings.Contains(out, "list ls -la") {
		t.Errorf("listFiles without path should fall back to command; got %q", out)
	}
}

func TestSplitProgramAndArgs(t *testing.T) {
	cases := []struct {
		in            string
		wantProg      string
		wantRest      string
	}{
		{"git log -6", "git", "log -6"},
		{"git", "git", ""},
		{"  git   status  ", "git", "status"},
		{"", "run", ""},
	}
	for _, tc := range cases {
		prog, rest := splitProgramAndArgs(tc.in)
		if prog != tc.wantProg || rest != tc.wantRest {
			t.Errorf("splitProgramAndArgs(%q)=(%q,%q) want (%q,%q)", tc.in, prog, rest, tc.wantProg, tc.wantRest)
		}
	}
}

func TestCodexCommandActions_ExtractsAndIgnoresGarbage(t *testing.T) {
	// Codex sends commandActions as []any of map[string]any. We extract
	// every well-formed entry and drop garbage (nil, wrong shape) so a
	// malformed entry can't break rendering for the rest.
	item := map[string]any{
		"type": "commandExecution",
		"commandActions": []any{
			map[string]any{"type": "read", "name": "a.go"},
			"not a map",
			nil,
			map[string]any{"type": "unknown", "command": "git status"},
		},
	}
	got := codexCommandActions(item)
	if len(got) != 2 {
		t.Fatalf("expected 2 well-formed actions; got %d (%+v)", len(got), got)
	}
	if t0, _ := got[0]["type"].(string); t0 != "read" {
		t.Errorf("got[0].type=%q want read", t0)
	}
	if t1, _ := got[1]["type"].(string); t1 != "unknown" {
		t.Errorf("got[1].type=%q want unknown", t1)
	}
}

func TestCodexCommandActions_MissingFieldReturnsNil(t *testing.T) {
	if got := codexCommandActions(map[string]any{"type": "commandExecution"}); got != nil {
		t.Errorf("missing commandActions should return nil; got %+v", got)
	}
	if got := codexCommandActions(map[string]any{"type": "commandExecution", "commandActions": []any{}}); got != nil {
		t.Errorf("empty commandActions should return nil; got %+v", got)
	}
}

func TestRenderToolResultBlock_TruncatesLongOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "line"+itoa(i))
	}
	out := renderToolResultBlock(strings.Join(lines, "\n"), false)
	if !strings.Contains(out, "more lines") {
		t.Errorf("long output should include truncation marker; got %q", out)
	}
	if strings.Contains(out, "line49") {
		t.Errorf("output beyond the cap should be trimmed; saw line49 in %q", out)
	}
}

func TestRenderToolResultBlock_ShortOutputUnchanged(t *testing.T) {
	out := renderToolResultBlock("one\ntwo", false)
	if !strings.Contains(out, "one") || !strings.Contains(out, "two") {
		t.Errorf("short output should render both lines; got %q", out)
	}
	if strings.Contains(out, "more lines") {
		t.Errorf("short output should not show truncation marker; got %q", out)
	}
}

func TestClampToolOutput_CharsCap(t *testing.T) {
	body := strings.Repeat("x", toolOutputMaxChars*2)
	kept, _ := clampToolOutput(body)
	if len(kept) > toolOutputMaxChars {
		t.Errorf("char cap not enforced: len=%d want <=%d", len(kept), toolOutputMaxChars)
	}
}

func TestClampToolOutput_LinesCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < toolOutputMaxLines*3; i++ {
		b.WriteString("a\n")
	}
	kept, trimmed := clampToolOutput(b.String())
	if strings.Count(kept, "\n") >= toolOutputMaxLines {
		// After TrimRight removes the trailing \n, line count should be
		// exactly toolOutputMaxLines (with toolOutputMaxLines-1 \n
		// separators).
		t.Errorf("expected line cap to leave at most %d lines; got %q", toolOutputMaxLines, kept)
	}
	if trimmed == 0 {
		t.Errorf("expected trimmed count > 0")
	}
}

func TestFormatToolInputValue_StringAndStruct(t *testing.T) {
	if got := formatToolInputValue("hello"); got != "hello" {
		t.Errorf("string should pass through; got %q", got)
	}
	if got := formatToolInputValue(map[string]any{"a": 1}); !strings.Contains(got, `"a":1`) {
		t.Errorf("map should JSON-encode; got %q", got)
	}
	if got := formatToolInputValue(nil); got != "null" {
		t.Errorf("nil should stringify as 'null'; got %q", got)
	}
}

func TestParseToolResultText_MapStdoutStderr(t *testing.T) {
	got, isErr, ok := parseToolResultText(map[string]any{
		"stdout": "ok",
		"stderr": "warn",
	})
	if !ok {
		t.Fatal("expected output")
	}
	if got != "ok\n\nwarn" {
		t.Fatalf("text=%q want %q", got, "ok\n\nwarn")
	}
	if !isErr {
		t.Fatal("stderr should mark isErr")
	}
}

func TestParseToolResultText_StringErrorPrefix(t *testing.T) {
	got, isErr, ok := parseToolResultText("Error: boom")
	if !ok || got != "Error: boom" || !isErr {
		t.Fatalf("got=(%q, %v, %v) want (Error: boom, true, true)", got, isErr, ok)
	}
}

func TestUserToolResult_FallbacksToMessageToolResult(t *testing.T) {
	ev := map[string]any{
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":     "tool_result",
					"content":  "hello from tool",
					"is_error": false,
				},
			},
		},
	}
	got, ok := userToolResult(ev)
	if !ok {
		t.Fatal("expected tool output from message block")
	}
	if got.output != "hello from tool" {
		t.Fatalf("got=%q want hello from tool", got.output)
	}
	if got.isError {
		t.Fatal("isErr should be false")
	}
}
