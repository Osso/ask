package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeStartup_FindsVSAndChdirs(t *testing.T) {
	isolateHome(t)
	ws := t.TempDir()
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", ws, "claude", "native-1", ws,
		"hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Move out of ws so chdir can be observed.
	t.Chdir(t.TempDir())

	got, err := resumeStartup(vsID)
	if err != nil {
		t.Fatalf("resumeStartup: %v", err)
	}
	if got != vsID {
		t.Errorf("returned id=%q want %q", got, vsID)
	}
	cur, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// macOS may resolve /var → /private/var; compare with EvalSymlinks.
	wantAbs, _ := filepath.EvalSymlinks(ws)
	gotAbs, _ := filepath.EvalSymlinks(cur)
	if gotAbs != wantAbs {
		t.Errorf("cwd=%q want %q", gotAbs, wantAbs)
	}
}

func TestResumeStartup_EmptyIDErrors(t *testing.T) {
	isolateHome(t)
	if _, err := resumeStartup(""); err == nil {
		t.Fatal("empty id should error")
	}
}

func TestResumeStartup_UnknownIDErrors(t *testing.T) {
	isolateHome(t)
	_, err := resumeStartup("vs-does-not-exist")
	if err == nil {
		t.Fatal("unknown vsID should error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should explain that VS is unknown, got %q", err)
	}
}

func TestResumeStartup_MissingWorkspaceErrors(t *testing.T) {
	isolateHome(t)
	missing := filepath.Join(t.TempDir(), "gone")
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", missing, "claude", "native-1",
		missing, "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := resumeStartup(vsID)
	if err == nil {
		t.Fatal("missing workspace should error")
	}
}

func TestResumeStartup_EmptyWorkspaceErrors(t *testing.T) {
	isolateHome(t)
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "", "claude", "native-1", "",
		"hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := resumeStartup(vsID)
	if err == nil {
		t.Fatal("empty workspace should error")
	}
}

func TestPrintHelp_MentionsKeyCommands(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	for _, want := range []string{"ask resume", "--help", "vs-"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q\n%s", want, out)
		}
	}
}

func TestPrintLastSession_PrintsActiveTabVID(t *testing.T) {
	first := newTabModelStub(t, 1, "vs-active")
	other := newTabModelStub(t, 2, "vs-other")
	a := app{tabs: []*model{first, other}, active: 0}
	var buf bytes.Buffer
	printLastSession(&buf, a)
	got := buf.String()
	if !strings.Contains(got, "last session: vs-active") {
		t.Errorf("expected active tab vsID in output, got %q", got)
	}
	if strings.Contains(got, "vs-other") {
		t.Errorf("only the active tab's vsID should be printed, got %q", got)
	}
}

func TestPrintLastSession_QuietWhenNoVID(t *testing.T) {
	tab := newTabModelStub(t, 1, "")
	a := app{tabs: []*model{tab}, active: 0}
	var buf bytes.Buffer
	printLastSession(&buf, a)
	if buf.Len() != 0 {
		t.Errorf("no vsID → no output, got %q", buf.String())
	}
}

func TestPrintLastSession_QuietWhenNoTabs(t *testing.T) {
	a := app{}
	var buf bytes.Buffer
	printLastSession(&buf, a)
	if buf.Len() != 0 {
		t.Errorf("no tabs → no output, got %q", buf.String())
	}
}

// newTabModelStub returns a minimal *model just rich enough for
// printLastSession to read its virtualSessionID; full model wiring
// (tea program, MCP bridge) is unnecessary for the printer test.
func newTabModelStub(t *testing.T, id int, vid string) *model {
	t.Helper()
	p := newFakeProvider()
	m := newTestModel(t, p)
	m.id = id
	m.virtualSessionID = vid
	return &m
}

func TestInit_EmitsStartupResumeWhenVSIDPreseeded(t *testing.T) {
	dir := initGitRepo(t)
	t.Chdir(dir)
	isolateHome(t)
	p := newFakeProvider()
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.cwd = dir
	m.virtualSessionID = "vs-pre-seeded"

	cmd := m.Init()
	msgs := drainBatch(t, cmd)
	var got *startupResumeMsg
	for _, msg := range msgs {
		if sr, ok := msg.(startupResumeMsg); ok {
			got = &sr
			break
		}
	}
	if got == nil {
		t.Fatalf("Init batch missing startupResumeMsg; got %v", msgs)
	}
	if got.tabID != m.id {
		t.Errorf("tabID=%d want %d", got.tabID, m.id)
	}
	if got.vsID != "vs-pre-seeded" {
		t.Errorf("vsID=%q want vs-pre-seeded", got.vsID)
	}
}

func TestInit_NoStartupResumeWhenVSIDEmpty(t *testing.T) {
	dir := initGitRepo(t)
	t.Chdir(dir)
	isolateHome(t)
	p := newFakeProvider()
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.cwd = dir

	cmd := m.Init()
	msgs := drainBatch(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(startupResumeMsg); ok {
			t.Errorf("Init must not emit startupResumeMsg without seeded vsID, got %T", msg)
		}
	}
}

func TestInit_NoStartupResumeWhenAlreadyHasSession(t *testing.T) {
	// Init runs again on Ctrl+T-style new tabs; virtualSessionID may
	// still carry over (it does, in the picker → swap path) but
	// sessionID being non-empty proves we're already attached, so the
	// startup-resume hook should stay quiet.
	dir := initGitRepo(t)
	t.Chdir(dir)
	isolateHome(t)
	p := newFakeProvider()
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.cwd = dir
	m.virtualSessionID = "vs-x"
	m.sessionID = "native-already-attached"

	cmd := m.Init()
	msgs := drainBatch(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(startupResumeMsg); ok {
			t.Error("startupResumeMsg should not fire when sessionID is already populated")
		}
	}
}

func TestUpdate_StartupResumeMsgRoutesIntoResumeVirtualSession(t *testing.T) {
	isolateHome(t)
	p := newFakeProvider()
	p.id = "claude"
	p.loadHistoryFn = func(id string, _ HistoryOpts) ([]historyEntry, error) {
		return []historyEntry{{kind: histUser, text: "loaded:" + id}}, nil
	}
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.cwd = "/ws"

	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "claude", "native-77",
		"/ws", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	newM, cmd := runUpdate(t, m, startupResumeMsg{tabID: m.id, vsID: vsID})
	if newM.virtualSessionID != vsID {
		t.Errorf("virtualSessionID=%q want %q", newM.virtualSessionID, vsID)
	}
	if newM.sessionID != "native-77" {
		t.Errorf("sessionID=%q want native-77", newM.sessionID)
	}
	if cmd == nil {
		t.Fatal("expected loadHistoryCmd, got nil")
	}
	hl, ok := cmd().(historyLoadedMsg)
	if !ok {
		t.Fatalf("expected historyLoadedMsg, got %T", cmd())
	}
	if hl.virtualSessionID != vsID {
		t.Errorf("historyLoadedMsg vsID=%q want %q", hl.virtualSessionID, vsID)
	}
}

func TestUpdate_StartupResumeMsgIgnoresWrongTab(t *testing.T) {
	isolateHome(t)
	p := newFakeProvider()
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.id = 7

	newM, cmd := runUpdate(t, m, startupResumeMsg{tabID: 99, vsID: "vs-wrong"})
	if cmd != nil {
		t.Errorf("wrong tab id should not produce a cmd, got %T", cmd)
	}
	if newM.virtualSessionID != "" {
		t.Errorf("wrong tab should not seed virtualSessionID, got %q", newM.virtualSessionID)
	}
}

