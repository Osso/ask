package main

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestParseUnifiedDiff_BasicHeader(t *testing.T) {
	diff := `@@ -1,3 +1,4 @@
 ctx
-old
+new
+added
`
	hunks := parseUnifiedDiff(diff)
	if len(hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]
	if h.oldStart != 1 || h.oldLines != 3 || h.newStart != 1 || h.newLines != 4 {
		t.Errorf("hunk header parsed wrong: %+v", h)
	}
	if len(h.lines) != 5 { // "ctx", "-old", "+new", "+added", ""
		t.Errorf("want 5 lines (inc trailing empty), got %d: %v", len(h.lines), h.lines)
	}
}

func TestParseUnifiedDiff_MultipleHunks(t *testing.T) {
	diff := `@@ -1,2 +1,2 @@
 a
-b
+c
@@ -10,1 +10,1 @@
-x
+y
`
	hunks := parseUnifiedDiff(diff)
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d: %+v", len(hunks), hunks)
	}
	if hunks[0].oldStart != 1 || hunks[1].oldStart != 10 {
		t.Errorf("second hunk starts at wrong offset: %+v", hunks)
	}
}

func TestParseUnifiedDiff_AbbreviatedHeader(t *testing.T) {
	// POSIX diff omits ",count" when count==1.
	diff := `@@ -42 +99 @@
-single
+replacement
`
	hunks := parseUnifiedDiff(diff)
	if len(hunks) != 1 {
		t.Fatalf("abbreviated header should still parse: %v", hunks)
	}
	h := hunks[0]
	if h.oldStart != 42 || h.newStart != 99 || h.oldLines != 1 || h.newLines != 1 {
		t.Errorf("abbreviated hunk parsed wrong: %+v", h)
	}
}

func TestParseUnifiedDiff_SkipsPreHeaderLines(t *testing.T) {
	diff := `--- a/f
+++ b/f
@@ -1 +1 @@
-a
+b
`
	hunks := parseUnifiedDiff(diff)
	if len(hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(hunks))
	}
	for _, ln := range hunks[0].lines {
		if strings.HasPrefix(ln, "---") || strings.HasPrefix(ln, "+++") {
			t.Errorf("file-header lines leaked into hunk: %q", ln)
		}
	}
}

func TestParseUnifiedDiff_EmptyIsNoOp(t *testing.T) {
	if h := parseUnifiedDiff(""); len(h) != 0 {
		t.Errorf("empty diff should produce no hunks, got %v", h)
	}
	if h := parseUnifiedDiff("just text without markers"); len(h) != 0 {
		t.Errorf("diff without @@ markers should produce no hunks, got %v", h)
	}
}

func TestCodexFileChanges_ExtractsEachFile(t *testing.T) {
	item := mustJSONMap(t, `{"type":"fileChange","id":"f1","changes":[
		{"path":"a.txt","diff":"@@ -1 +1 @@\n-old\n+new\n","kind":"update"},
		{"path":"b.txt","diff":"@@ -0,0 +1,1 @@\n+new file\n","kind":"add"},
		{"path":"skip.txt","diff":"","kind":"delete"}
	],"status":"applied"}`)
	diffs := codexFileChanges(item)
	if len(diffs) != 2 {
		t.Fatalf("want 2 diffs (empty one skipped), got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "a.txt" || diffs[1].Path != "b.txt" {
		t.Errorf("paths wrong: %v", diffs)
	}
}

func TestCodexStream_FileChangeCompletionEmitsToolDiffMsg(t *testing.T) {
	proc := &providerProc{}
	ev := mustJSONMap(t, `{"method":"item/completed","params":{"item":{
		"type":"fileChange","id":"f","status":"applied","changes":[
			{"path":"a.go","diff":"@@ -1 +1 @@\n-old\n+new\n","kind":"update"}
		]
	}}}`)
	msgs := codexEventToMsgs(ev, proc)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d: %v", len(msgs), msgs)
	}
	d, ok := msgs[0].(toolDiffMsg)
	if !ok {
		t.Fatalf("msg[0] %T want toolDiffMsg", msgs[0])
	}
	if d.filePath != "a.go" {
		t.Errorf("filePath=%q want a.go", d.filePath)
	}
	if len(d.hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(d.hunks))
	}
}

func TestCodexStream_TurnStartedCapturesTurnID(t *testing.T) {
	state := &codexState{}
	proc := &providerProc{payload: state}
	ev := mustJSONMap(t, `{"method":"turn/started","params":{"threadId":"t","turn":{"id":"turn-abc"}}}`)
	_ = codexEventToMsgs(ev, proc)
	if state.currentTurnID != "turn-abc" {
		t.Errorf("turn id not captured: got %q want turn-abc", state.currentTurnID)
	}
}

func TestCodexStream_TurnCompletedClearsTurnID(t *testing.T) {
	// Regression guard: without this clear, a stale turnID could
	// make a subsequent Ctrl+C send turn/interrupt for a turn that
	// already ended, leaving the UI stuck in "cancelling…".
	state := &codexState{currentTurnID: "turn-abc"}
	proc := &providerProc{payload: state}
	ev := mustJSONMap(t, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"turn-abc","status":"completed"}}}`)
	_ = codexEventToMsgs(ev, proc)
	if state.currentTurnID != "" {
		t.Errorf("currentTurnID should clear on turn/completed, got %q", state.currentTurnID)
	}
}

// mustJSONMap is a tiny assert helper so the diff test file isn't
// awash in json.Unmarshal boilerplate.
func mustJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", raw, err)
	}
	return m
}

// Compile-time check that tea.Msg is still the interface codex emits.
var _ tea.Msg = toolDiffMsg{}
