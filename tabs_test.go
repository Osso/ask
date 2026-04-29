package main

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// preserveCwd snapshots the test process cwd and restores it on cleanup.
// app.openTab / app.closeTab mutate os.Chdir, so any test that exercises
// those paths must isolate later tests from cwd drift.
func preserveCwd(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		return
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func testAppWithTwoTabs(t *testing.T) app {
	t.Helper()
	first := newTestModel(t, newFakeProvider())
	second := newTestModel(t, newFakeProvider())
	second.id = 2
	return app{
		tabs:   []*model{&first, &second},
		active: 1,
		nextID: 3,
		width:  first.width,
		height: first.height,
	}
}

func TestApp_SessionsLoadedStaysOnOwningTab(t *testing.T) {
	a := testAppWithTwoTabs(t)

	newA, _ := a.Update(sessionsLoadedMsg{
		tabID:    a.tabs[0].id,
		sessions: []sessionEntry{{id: "A"}, {id: "B"}},
	})
	a2 := newA.(app)

	if a2.tabs[0].mode != modeSessionPicker {
		t.Errorf("tab 1 mode=%v want modeSessionPicker", a2.tabs[0].mode)
	}
	if a2.tabs[1].mode != modeInput {
		t.Errorf("tab 2 mode=%v want modeInput", a2.tabs[1].mode)
	}
}

func TestApp_CtrlDExitsShellModeFirst(t *testing.T) {
	a := testAppWithTwoTabs(t)
	a.tabs[a.active].shellMode = true

	newM, cmd := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'd'})
	a2 := newM.(app)
	if a2.tabs[a2.active].shellMode {
		t.Fatal("ctrl+d in shell mode should clear shellMode")
	}
	if len(a2.tabs) != 2 {
		t.Fatalf("ctrl+d in shell mode must not close a tab; got %d tabs", len(a2.tabs))
	}
	if cmd != nil {
		if msg, ok := cmd().(tea.QuitMsg); ok {
			t.Fatalf("ctrl+d in shell mode must not quit; got %T", msg)
		}
	}
}

func TestApp_CtrlDClosesTabWhenMultipleOpen(t *testing.T) {
	preserveCwd(t)
	a := testAppWithTwoTabs(t)
	closingID := a.tabs[a.active].id

	newM, _ := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'd'})
	a2 := newM.(app)
	if len(a2.tabs) != 1 {
		t.Fatalf("ctrl+d with multiple tabs should close the active tab; got %d tabs", len(a2.tabs))
	}
	if a2.tabs[0].id == closingID {
		t.Fatalf("ctrl+d should have removed tab id %d", closingID)
	}
}

func TestApp_CtrlDQuitsOnLastTab(t *testing.T) {
	first := newTestModel(t, newFakeProvider())
	a := app{
		tabs:   []*model{&first},
		active: 0,
		nextID: 2,
		width:  first.width,
		height: first.height,
	}

	_, cmd := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'd'})
	if cmd == nil {
		t.Fatal("ctrl+d on last tab should return tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+d on last tab should return tea.QuitMsg, got %T", cmd())
	}
}

func TestApp_CtrlDQuitAcceptsControlCodeShape(t *testing.T) {
	first := newTestModel(t, newFakeProvider())
	a := app{
		tabs:   []*model{&first},
		active: 0,
		nextID: 2,
		width:  first.width,
		height: first.height,
	}

	_, cmd := a.Update(tea.KeyPressMsg{Code: 0x04})
	if cmd == nil {
		t.Fatal("ctrl+d control-code shape should return tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+d control-code shape should return tea.QuitMsg, got %T", cmd())
	}
}

func TestApp_CtrlNOpensNewTab(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"mod-ctrl-n", tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'n'}},
		{"raw-ctrl-code", tea.KeyPressMsg{Code: 0x0E}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preserveCwd(t)
			first := newTestModel(t, newFakeProvider())
			a := app{
				tabs:   []*model{&first},
				active: 0,
				nextID: 2,
				width:  first.width,
				height: first.height,
			}

			newM, _ := a.Update(tc.msg)
			a2 := newM.(app)
			if len(a2.tabs) != 2 {
				t.Fatalf("ctrl+n should open a new tab, got %d tabs", len(a2.tabs))
			}
			if a2.active != 1 {
				t.Errorf("ctrl+n should focus the new tab, active=%d want 1", a2.active)
			}
		})
	}
}

// Tab switching is bound to ctrl+shift+pgup / ctrl+shift+pgdown so
// ctrl+left/right stays free for textarea word motion. The legacy
// ctrl+left/right bindings must NOT switch tabs anymore.
func TestApp_CtrlShiftPgUpPgDownSwitchesTabs(t *testing.T) {
	preserveCwd(t)
	a := testAppWithTwoTabs(t)
	if a.active != 1 {
		t.Fatalf("precondition: testAppWithTwoTabs starts on tab 1, got active=%d", a.active)
	}

	newM, _ := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl | tea.ModShift, Code: tea.KeyPgUp})
	a2 := newM.(app)
	if a2.active != 0 {
		t.Fatalf("ctrl+shift+pgup should move to previous tab; active=%d want 0", a2.active)
	}

	newM, _ = a2.Update(tea.KeyPressMsg{Mod: tea.ModCtrl | tea.ModShift, Code: tea.KeyPgDown})
	a3 := newM.(app)
	if a3.active != 1 {
		t.Fatalf("ctrl+shift+pgdown should move to next tab; active=%d want 1", a3.active)
	}
}

func TestApp_CtrlLeftRightDoesNotSwitchTabs(t *testing.T) {
	preserveCwd(t)
	a := testAppWithTwoTabs(t)
	startActive := a.active

	for _, msg := range []tea.KeyPressMsg{
		{Mod: tea.ModCtrl, Code: tea.KeyLeft},
		{Mod: tea.ModCtrl, Code: tea.KeyRight},
	} {
		newM, _ := a.Update(msg)
		a2 := newM.(app)
		if a2.active != startActive {
			t.Errorf("%+v should be forwarded to the active tab, not switch tabs; active=%d want %d", msg, a2.active, startActive)
		}
	}
}

func TestApp_CtrlTDoesNotOpenNewTab(t *testing.T) {
	first := newTestModel(t, newFakeProvider())
	a := app{
		tabs:   []*model{&first},
		active: 0,
		nextID: 2,
		width:  first.width,
		height: first.height,
	}

	newM, _ := a.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 't'})
	a2 := newM.(app)
	if len(a2.tabs) != 1 {
		t.Fatalf("ctrl+t must no longer open a tab; got %d tabs", len(a2.tabs))
	}
}

func TestApp_HistoryLoadedStaysOnOwningTab(t *testing.T) {
	a := testAppWithTwoTabs(t)
	a.tabs[0].sessionID = "shared"
	a.tabs[0].virtualSessionID = "vs-shared"
	a.tabs[1].sessionID = "shared"
	a.tabs[1].virtualSessionID = "vs-shared"
	a.tabs[1].history = []historyEntry{{kind: histUser, text: "keep"}}

	newA, _ := a.Update(historyLoadedMsg{
		tabID:            a.tabs[0].id,
		sessionID:        "shared",
		virtualSessionID: "vs-shared",
		entries:          []historyEntry{{kind: histUser, text: "owner-only"}},
		silent:           true,
	})
	a2 := newA.(app)

	if len(a2.tabs[0].history) != 1 || a2.tabs[0].history[0].text != "owner-only" {
		t.Errorf("tab 1 history=%+v want owner-only payload", a2.tabs[0].history)
	}
	if len(a2.tabs[1].history) != 1 || a2.tabs[1].history[0].text != "keep" {
		t.Errorf("tab 2 history=%+v want unchanged history", a2.tabs[1].history)
	}
}

func TestApp_ProviderInitLoadedStaysOnOwningTab(t *testing.T) {
	a := testAppWithTwoTabs(t)
	a.tabs[0].providerSlashCmds = []providerSlashEntry{{Name: "one"}}
	a.tabs[1].providerSlashCmds = []providerSlashEntry{{Name: "two"}}

	newA, _ := a.Update(providerInitLoadedMsg{
		tabID:     a.tabs[0].id,
		slashCmds: []providerSlashEntry{{Name: "resume"}, {Name: "config"}},
	})
	a2 := newA.(app)

	if len(a2.tabs[0].providerSlashCmds) != 2 {
		t.Errorf("tab 1 slash cmds=%+v want updated entries", a2.tabs[0].providerSlashCmds)
	}
	if len(a2.tabs[1].providerSlashCmds) != 1 || a2.tabs[1].providerSlashCmds[0].Name != "two" {
		t.Errorf("tab 2 slash cmds=%+v want unchanged entries", a2.tabs[1].providerSlashCmds)
	}
}

func TestApp_CtrlZSuspendsAndRendersInlineBackgroundedMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{name: "mod-ctrl-z", msg: tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}},
		{name: "raw-ctrl-code", msg: tea.KeyPressMsg{Code: 0x1A}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAppWithTwoTabs(t)
			a.tabs[0].input.SetValue("inactive draft")
			a.tabs[1].input.SetValue("active draft")

			newA, cmd := a.Update(tc.msg)
			a2, ok := newA.(app)
			if !ok {
				t.Fatalf("Update returned %T, want app", newA)
			}

			if cmd == nil {
				t.Fatal("Ctrl+Z must return tea.Suspend; got nil cmd")
			}
			if msg := cmd(); msg != (tea.SuspendMsg{}) {
				t.Errorf("cmd should yield tea.SuspendMsg{}, got %T %+v", msg, msg)
			}
			if !a2.suspending {
				t.Error("app.suspending must be true between Ctrl+Z and ResumeMsg")
			}

			// View while suspending must render *inline* (no altscreen), so
			// the message lands in the user's terminal scrollback once
			// bubbletea exits altscreen and SIGTSTP fires.
			view := a2.View()
			if view.AltScreen {
				t.Error("suspending View must have AltScreen=false to print to the host terminal")
			}
			if !strings.Contains(view.Content, "backgrounded") {
				t.Errorf("suspending View content=%q must mention 'backgrounded'", view.Content)
			}
			if !strings.Contains(view.Content, "fg") {
				t.Errorf("suspending View content=%q should hint at `fg` resume", view.Content)
			}

			// Neither tab's history should be touched — the message lives in
			// the shell, not in ask.
			for i, tb := range a2.tabs {
				if len(tb.history) != 0 {
					t.Errorf("tab %d history must stay empty; got %+v", i, tb.history)
				}
			}

			// The keypress must be consumed by the app, not forwarded to the
			// active tab's textarea — otherwise a stray 'z' would land in the
			// input on every suspend.
			active := a2.tabs[a2.active]
			inactive := a2.tabs[1-a2.active]
			if got := active.input.Value(); got != "active draft" {
				t.Errorf("active tab input mutated by Ctrl+Z: got %q want %q", got, "active draft")
			}
			if got := inactive.input.Value(); got != "inactive draft" {
				t.Errorf("inactive tab input mutated by Ctrl+Z: got %q want %q", got, "inactive draft")
			}
		})
	}
}

// Ctrl+Z must suspend even when the active tab is in a non-input mode
// (modal, picker, shell). We exercise the modal path here as a stand-in
// for "any non-input mode" — the dispatch happens at the app layer
// before mode-specific routing, so all of them share the same gate.
func TestApp_CtrlZSuspendsRegardlessOfActiveTabMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{name: "mod-ctrl-z", msg: tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}},
		{name: "raw-ctrl-code", msg: tea.KeyPressMsg{Code: 0x1A}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAppWithTwoTabs(t)
			a.tabs[a.active].mode = modeAskQuestion

			_, cmd := a.Update(tc.msg)
			if cmd == nil {
				t.Fatal("Ctrl+Z in modal mode must still return tea.Suspend")
			}
			if msg := cmd(); msg != (tea.SuspendMsg{}) {
				t.Errorf("cmd should yield tea.SuspendMsg{}, got %T", msg)
			}
		})
	}
}

// ResumeMsg (delivered by bubbletea after SIGCONT) must clear the
// suspending flag so the next View re-enters altscreen and renders
// the live TUI instead of the inline backgrounded message.
func TestApp_ResumeMsgClearsSuspendingAndReturnsToAltScreen(t *testing.T) {
	a := testAppWithTwoTabs(t)
	a.suspending = true

	newA, _ := a.Update(tea.ResumeMsg{})
	a2, ok := newA.(app)
	if !ok {
		t.Fatalf("Update returned %T, want app", newA)
	}
	if a2.suspending {
		t.Error("ResumeMsg must clear suspending")
	}
	view := a2.View()
	if !view.AltScreen {
		t.Error("post-resume View must re-enter altscreen")
	}
	if strings.Contains(view.Content, "backgrounded") {
		t.Errorf("post-resume View should not show backgrounded message; content=%q", view.Content)
	}
}

func TestApp_VirtualSessionMaterializedStaysOnOwningTab(t *testing.T) {
	a := testAppWithTwoTabs(t)
	a.tabs[0].virtualSessionID = "vs-shared"
	a.tabs[0].busy = true
	a.tabs[1].virtualSessionID = "vs-shared"
	a.tabs[1].busy = true
	a.tabs[1].sessionID = "keep"

	newA, _ := a.Update(virtualSessionMaterializedMsg{
		tabID:           a.tabs[0].id,
		vsID:            "vs-shared",
		nativeSessionID: "owner-session",
		nativeCwd:       "/owner",
		entries:         []historyEntry{{kind: histUser, text: "translated"}},
	})
	a2 := newA.(app)

	if a2.tabs[0].sessionID != "owner-session" || a2.tabs[0].resumeCwd != "/owner" || a2.tabs[0].busy {
		t.Errorf("tab 1 translate state=%+v want owner session applied and busy cleared", *a2.tabs[0])
	}
	if a2.tabs[1].sessionID != "keep" || !a2.tabs[1].busy {
		t.Errorf("tab 2 state mutated unexpectedly: session=%q busy=%v", a2.tabs[1].sessionID, a2.tabs[1].busy)
	}
}
