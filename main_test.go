package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestResumeLookup_FindsVSAndReturnsWorkspace(t *testing.T) {
	isolateHome(t)
	ws := t.TempDir()
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", ws, "claude", "native-1", ws,
		"hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	gotID, gotWS, gotProv, err := resumeLookup(vsID)
	if err != nil {
		t.Fatalf("resumeLookup: %v", err)
	}
	if gotID != vsID {
		t.Errorf("returned id=%q want %q", gotID, vsID)
	}
	if gotProv != "claude" {
		t.Errorf("returned lastProvider=%q want claude", gotProv)
	}
	wantAbs, _ := filepath.EvalSymlinks(ws)
	gotAbs, _ := filepath.EvalSymlinks(gotWS)
	if gotAbs != wantAbs {
		t.Errorf("returned workspace=%q want %q", gotAbs, wantAbs)
	}
}

func TestResumeLookup_ReturnsLastProviderForCodexVS(t *testing.T) {
	isolateHome(t)
	ws := t.TempDir()
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", ws, "codex", "native-cdx", ws,
		"hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, _, gotProv, err := resumeLookup(vsID)
	if err != nil {
		t.Fatalf("resumeLookup: %v", err)
	}
	if gotProv != "codex" {
		t.Errorf("lastProvider=%q want codex", gotProv)
	}
}

func TestResumeLookup_LegacyVSWithoutLastProviderReturnsEmpty(t *testing.T) {
	isolateHome(t)
	ws := t.TempDir()
	store := &virtualSessionStore{Version: 1, Sessions: []VirtualSession{{
		ID:        "vs-legacy",
		Workspace: ws,
		// LastProvider intentionally omitted to simulate a VS written
		// before that field was tracked.
		ProviderSessions: map[string]ProviderSessionRef{
			"claude": {SessionID: "native-x", Cwd: ws},
		},
	}}}
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, _, gotProv, err := resumeLookup("vs-legacy")
	if err != nil {
		t.Fatalf("resumeLookup: %v", err)
	}
	if gotProv != "" {
		t.Errorf("legacy VS lastProvider=%q want empty", gotProv)
	}
}

func TestResumeLookup_EmptyIDErrors(t *testing.T) {
	isolateHome(t)
	if _, _, _, err := resumeLookup(""); err == nil {
		t.Fatal("empty id should error")
	}
}

func TestResumeLookup_UnknownIDErrors(t *testing.T) {
	isolateHome(t)
	_, _, _, err := resumeLookup("vs-does-not-exist")
	if err == nil {
		t.Fatal("unknown vsID should error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should explain that VS is unknown, got %q", err)
	}
}

func TestResumeLookup_MissingWorkspaceErrors(t *testing.T) {
	isolateHome(t)
	missing := filepath.Join(t.TempDir(), "gone")
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", missing, "claude", "native-1",
		missing, "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, _, _, err := resumeLookup(vsID)
	if err == nil {
		t.Fatal("missing workspace should error")
	}
}

func TestResumeLookup_EmptyWorkspaceErrors(t *testing.T) {
	isolateHome(t)
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "", "claude", "native-1", "",
		"hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, _, _, err := resumeLookup(vsID)
	if err == nil {
		t.Fatal("empty workspace should error")
	}
}

func TestPrintHelp_MentionsKeyCommands(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	for _, want := range []string{"ask resume", "--help", "vs-", "--simulate-approval", "--provider", "--model"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q\n%s", want, out)
		}
	}
}

func TestParseSimulateApprovalFlag(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantOn      bool
		wantTool    string
		wantRest    []string
	}{
		{
			name:     "absent",
			args:     []string{"resume", "vs-abc"},
			wantOn:   false,
			wantTool: "Bash",
			wantRest: []string{"resume", "vs-abc"},
		},
		{
			name:     "bare flag defaults to Bash",
			args:     []string{"--simulate-approval"},
			wantOn:   true,
			wantTool: "Bash",
			wantRest: []string{},
		},
		{
			name:     "flag with tool",
			args:     []string{"--simulate-approval=Edit"},
			wantOn:   true,
			wantTool: "Edit",
			wantRest: []string{},
		},
		{
			name:     "flag mixed with other args is stripped",
			args:     []string{"resume", "vs-1", "--simulate-approval=WebFetch"},
			wantOn:   true,
			wantTool: "WebFetch",
			wantRest: []string{"resume", "vs-1"},
		},
		{
			name:     "empty value falls back to default",
			args:     []string{"--simulate-approval="},
			wantOn:   true,
			wantTool: "Bash",
			wantRest: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOn, gotTool, gotRest := parseSimulateApprovalFlag(tc.args)
			if gotOn != tc.wantOn {
				t.Errorf("on=%v want %v", gotOn, tc.wantOn)
			}
			if gotTool != tc.wantTool {
				t.Errorf("tool=%q want %q", gotTool, tc.wantTool)
			}
			if !equalStrSlice(gotRest, tc.wantRest) {
				t.Errorf("rest=%v want %v", gotRest, tc.wantRest)
			}
		})
	}
}

func TestParseProviderModelFlags(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantProvider string
		wantModel    string
		wantRest     []string
		wantErr      string
	}{
		{
			name:     "absent leaves args untouched",
			args:     []string{"resume", "vs-1"},
			wantRest: []string{"resume", "vs-1"},
		},
		{
			name:         "provider space form",
			args:         []string{"--provider", "codex"},
			wantProvider: "codex",
			wantRest:     []string{},
		},
		{
			name:         "provider equals form",
			args:         []string{"--provider=codex"},
			wantProvider: "codex",
			wantRest:     []string{},
		},
		{
			name:      "model space form",
			args:      []string{"--model", "haiku"},
			wantModel: "haiku",
			wantRest:  []string{},
		},
		{
			name:      "model equals form",
			args:      []string{"--model=opus"},
			wantModel: "opus",
			wantRest:  []string{},
		},
		{
			name:         "both flags mixed with positional",
			args:         []string{"--provider", "claude", "resume", "vs-x", "--model=opus"},
			wantProvider: "claude",
			wantModel:    "opus",
			wantRest:     []string{"resume", "vs-x"},
		},
		{
			name:    "bare --provider errors",
			args:    []string{"--provider"},
			wantErr: "--provider: missing value",
		},
		{
			name:    "empty --provider= errors",
			args:    []string{"--provider="},
			wantErr: "--provider: missing value",
		},
		{
			name:    "bare --model errors",
			args:    []string{"--model"},
			wantErr: "--model: missing value",
		},
		{
			name:    "empty --model= errors",
			args:    []string{"--model="},
			wantErr: "--model: missing value",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotProv, gotModel, gotRest, err := parseProviderModelFlags(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%q want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotProv != tc.wantProvider {
				t.Errorf("provider=%q want %q", gotProv, tc.wantProvider)
			}
			if gotModel != tc.wantModel {
				t.Errorf("model=%q want %q", gotModel, tc.wantModel)
			}
			if !equalStrSlice(gotRest, tc.wantRest) {
				t.Errorf("rest=%v want %v", gotRest, tc.wantRest)
			}
		})
	}
}

func TestStrictProviderByID(t *testing.T) {
	a := newFakeProvider()
	a.id = "alpha"
	b := newFakeProvider()
	b.id = "beta"
	withRegisteredProviders(t, a, b)

	if got := strictProviderByID("beta"); got == nil || got.ID() != "beta" {
		t.Errorf("strictProviderByID(beta)=%v want beta", got)
	}
	if got := strictProviderByID("nope"); got != nil {
		t.Errorf("strictProviderByID(nope)=%v want nil (no fallback)", got)
	}
	if got := strictProviderByID(""); got != nil {
		t.Errorf("strictProviderByID(\"\")=%v want nil (no fallback)", got)
	}
}

func TestParseCLICommand(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    cliCommand
		wantErr string
	}{
		{name: "no args runs TUI", args: nil, want: cliCommand{Kind: "run"}},
		{name: "empty slice runs TUI", args: []string{}, want: cliCommand{Kind: "run"}},
		{name: "help long", args: []string{"--help"}, want: cliCommand{Kind: "help"}},
		{name: "help short", args: []string{"-h"}, want: cliCommand{Kind: "help"}},
		{name: "help word", args: []string{"help"}, want: cliCommand{Kind: "help"}},
		{
			name: "resume with vid",
			args: []string{"resume", "vs-abc"},
			want: cliCommand{Kind: "resume", VSID: "vs-abc"},
		},
		{
			name:    "resume missing vid",
			args:    []string{"resume"},
			wantErr: "missing virtual session id",
		},
		{
			name:    "resume too many args",
			args:    []string{"resume", "vs-abc", "extra"},
			wantErr: "unexpected extra arguments",
		},
		{
			name:    "help with extra arg",
			args:    []string{"--help", "junk"},
			wantErr: "unexpected arguments",
		},
		{
			name:    "unknown long flag",
			args:    []string{"--frobnicate"},
			wantErr: "unknown option: --frobnicate",
		},
		{
			name:    "unknown short flag",
			args:    []string{"-x"},
			wantErr: "unknown option: -x",
		},
		{
			name:    "unknown subcommand",
			args:    []string{"banana"},
			wantErr: "unknown argument: banana",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCLICommand(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (cmd=%+v)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%q want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestSimulatedApprovalInput_TargetsKnownTools(t *testing.T) {
	cases := map[string]string{
		"Bash":     "command",
		"Edit":     "file_path",
		"Read":     "file_path",
		"Glob":     "pattern",
		"WebFetch": "url",
	}
	for tool, key := range cases {
		t.Run(tool, func(t *testing.T) {
			in := simulatedApprovalInput(tool)
			if _, ok := in[key]; !ok {
				t.Errorf("simulated input for %q missing key %q: %#v", tool, key, in)
			}
		})
	}
	if got := simulatedApprovalInput("Mystery"); len(got) != 0 {
		t.Errorf("unknown tool should produce empty map, got %#v", got)
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Closing the last tab must arm the quitting flag with the active
// tab's virtualSessionID; the next View renders inline so the line
// lands in the host shell's scrollback after altscreen tears down.
// Mirrors how Ctrl+Z's suspending flag works.
func TestCloseLastTab_ArmsQuittingWithVID(t *testing.T) {
	tab := newTabModelStub(t, 1, "vs-active")
	a := app{tabs: []*model{tab}, active: 0}

	newA, cmd := a.closeTab(1)
	a2, ok := newA.(app)
	if !ok {
		t.Fatalf("closeTab returned %T, want app", newA)
	}
	if cmd == nil {
		t.Fatal("closing the last tab must return a quit cmd")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Errorf("cmd should yield tea.QuitMsg{}, got %T %+v", msg, msg)
	}
	if !a2.quitting {
		t.Error("a.quitting must be true between last-tab-close and QuitMsg")
	}
	if a2.quittingVID != "vs-active" {
		t.Errorf("quittingVID=%q want vs-active", a2.quittingVID)
	}

	// View while quitting must render *inline* (no altscreen) so the
	// content survives the cursed_renderer.close → EraseScreenBelow
	// teardown into the host shell's scrollback.
	view := a2.View()
	if view.AltScreen {
		t.Error("quitting View must have AltScreen=false")
	}
	if !strings.Contains(view.Content, "last session: vs-active") {
		t.Errorf("quitting View content=%q must announce the vsID", view.Content)
	}
}

func TestCloseLastTab_NoVIDLeavesQuittingDisarmed(t *testing.T) {
	tab := newTabModelStub(t, 1, "")
	a := app{tabs: []*model{tab}, active: 0}

	newA, cmd := a.closeTab(1)
	a2 := newA.(app)
	if cmd == nil {
		t.Fatal("closing the last tab must still return tea.Quit")
	}
	if a2.quitting {
		t.Error("no vsID → don't flicker the quitting render path")
	}
	if a2.quittingVID != "" {
		t.Errorf("quittingVID should stay empty, got %q", a2.quittingVID)
	}
	view := a2.View()
	if !view.AltScreen {
		t.Error("View without quitting must keep AltScreen=true (normal render)")
	}
}

// Closing a non-last tab must not arm the quit flag; the program
// stays alive on the surviving tabs.
func TestCloseTab_NonLastTabDoesNotArmQuitting(t *testing.T) {
	// closeTab(non-last) follows the new active tab's cwd via os.Chdir.
	// Pin our own cwd via t.Chdir so the cleanup restores it — the
	// production chdir is fine for a real session but pollutes every
	// later test in the same process.
	t.Chdir(t.TempDir())

	first := newTabModelStub(t, 1, "vs-first")
	second := newTabModelStub(t, 2, "vs-second")
	a := app{tabs: []*model{first, second}, active: 0, width: 100, height: 30}

	newA, _ := a.closeTab(1)
	a2 := newA.(app)
	if a2.quitting {
		t.Error("closing one of two tabs must not arm quitting")
	}
	if a2.quittingVID != "" {
		t.Errorf("quittingVID should stay empty, got %q", a2.quittingVID)
	}
}

// newTabModelStub returns a minimal *model just rich enough for the
// app-level close/View tests to read its virtualSessionID and run
// killProc/drainPendingReplies as no-ops; full model wiring
// (tea program, MCP bridge) is unnecessary at this layer.
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

