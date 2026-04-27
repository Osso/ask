package main

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// captureTermCwd swaps emitTermCwdFunc with a recorder for the lifetime
// of the test and returns a pointer to the captured paths slice.
func captureTermCwd(t *testing.T) *[]string {
	t.Helper()
	orig := emitTermCwdFunc
	captured := &[]string{}
	emitTermCwdFunc = func(path string) {
		*captured = append(*captured, path)
	}
	t.Cleanup(func() { emitTermCwdFunc = orig })
	return captured
}

func TestEffectiveCwd_ReturnsWorktreePathWhenNamed(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.cwd = "/repo"
	m.worktreeName = "alpha-going-river"
	got := m.effectiveCwd()
	want := filepath.Join("/repo", ".claude", "worktrees", "alpha-going-river")
	if got != want {
		t.Errorf("effectiveCwd=%q want %q", got, want)
	}
}

func TestEffectiveCwd_FallsBackToCwd(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.cwd = "/repo"
	m.worktreeName = ""
	if got := m.effectiveCwd(); got != "/repo" {
		t.Errorf("effectiveCwd=%q want /repo", got)
	}
}

func TestEmitTermCwd_SkipsEmpty(t *testing.T) {
	captured := captureTermCwd(t)
	emitTermCwd("")
	if len(*captured) != 0 {
		t.Errorf("emitTermCwd(\"\") must not call emitter, got %v", *captured)
	}
}

func TestSyncTermCwd_EmitsActiveTabEffectiveCwd(t *testing.T) {
	captured := captureTermCwd(t)
	first := newTestModel(t, newFakeProvider())
	first.cwd = "/repo"
	first.worktreeName = "alpha-going-river"
	a := app{tabs: []*model{&first}, active: 0, nextID: 2}
	a.syncTermCwd()
	if len(*captured) != 1 {
		t.Fatalf("syncTermCwd should emit once, got %v", *captured)
	}
	want := filepath.Join("/repo", ".claude", "worktrees", "alpha-going-river")
	if (*captured)[0] != want {
		t.Errorf("emitted=%q want %q", (*captured)[0], want)
	}
}

func TestAppUpdate_EmitsWhenWorktreeNameLandsOnActiveTab(t *testing.T) {
	captured := captureTermCwd(t)
	a := testAppWithTwoTabs(t)
	a.tabs[a.active].cwd = "/repo"
	// procStarting needed so handleProviderStartDone accepts the message.
	a.tabs[a.active].procStarting = true
	a.tabs[a.active].procStartSeq = 1
	fp := a.tabs[a.active].provider.(*fakeProvider)

	proc := &providerProc{}
	newM, _ := a.Update(providerStartDoneMsg{
		tabID:        a.tabs[a.active].id,
		seq:          1,
		providerID:   fp.id,
		proc:         proc,
		worktreeName: "merry-floating-loon",
	})
	a2 := newM.(app)
	if a2.tabs[a2.active].worktreeName != "merry-floating-loon" {
		t.Fatalf("worktreeName not stored: %q", a2.tabs[a2.active].worktreeName)
	}
	if len(*captured) == 0 {
		t.Fatal("expected OSC 7 emit when worktreeName landed on active tab")
	}
	want := filepath.Join("/repo", ".claude", "worktrees", "merry-floating-loon")
	if last := (*captured)[len(*captured)-1]; last != want {
		t.Errorf("last emit=%q want %q", last, want)
	}
}

func TestAppUpdate_DoesNotEmitWhenWorktreeNameLandsOnInactiveTab(t *testing.T) {
	captured := captureTermCwd(t)
	a := testAppWithTwoTabs(t)
	// Make active and inactive share the same effective cwd so any emit
	// on this path is unambiguously caused by the inactive tab change.
	a.tabs[0].cwd = "/repo"
	a.tabs[1].cwd = "/repo"
	a.active = 0
	// Send providerStartDoneMsg targeting the inactive (second) tab.
	a.tabs[1].procStarting = true
	a.tabs[1].procStartSeq = 1
	fp := a.tabs[1].provider.(*fakeProvider)

	proc := &providerProc{}
	newM, _ := a.Update(providerStartDoneMsg{
		tabID:        a.tabs[1].id,
		seq:          1,
		providerID:   fp.id,
		proc:         proc,
		worktreeName: "calm-walking-doe",
	})
	a2 := newM.(app)
	if a2.tabs[1].worktreeName != "calm-walking-doe" {
		t.Fatalf("inactive tab worktreeName not stored: %q", a2.tabs[1].worktreeName)
	}
	if len(*captured) != 0 {
		t.Errorf("inactive-tab worktree change must not emit OSC 7, got %v", *captured)
	}
}

func TestAppUpdate_EmitsOnTabSwitchWhenEffectiveCwdDiffers(t *testing.T) {
	captured := captureTermCwd(t)
	a := testAppWithTwoTabs(t)
	a.tabs[0].cwd = "/repo"
	a.tabs[1].cwd = "/repo"
	a.tabs[1].worktreeName = "shimmering-flying-crow"
	a.active = 0

	newM, _ := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: tea.KeyRight})
	a2 := newM.(app)
	if a2.active != 1 {
		t.Fatalf("ctrl+right should focus tab 2, active=%d", a2.active)
	}
	if len(*captured) != 1 {
		t.Fatalf("tab switch with differing effective cwd should emit once, got %v", *captured)
	}
	want := filepath.Join("/repo", ".claude", "worktrees", "shimmering-flying-crow")
	if (*captured)[0] != want {
		t.Errorf("emitted=%q want %q", (*captured)[0], want)
	}
}

func TestAppUpdate_DoesNotEmitOnTabSwitchWhenEffectiveCwdMatches(t *testing.T) {
	captured := captureTermCwd(t)
	a := testAppWithTwoTabs(t)
	a.tabs[0].cwd = "/repo"
	a.tabs[1].cwd = "/repo"
	a.active = 0

	newM, _ := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: tea.KeyRight})
	a2 := newM.(app)
	if a2.active != 1 {
		t.Fatalf("ctrl+right should focus tab 2, active=%d", a2.active)
	}
	if len(*captured) != 0 {
		t.Errorf("tab switch with same effective cwd must not emit, got %v", *captured)
	}
}
