package main

import (
	"strings"
	"testing"
	"time"
)

func TestToastRender_NoActiveAlertReturnsInputUnchanged(t *testing.T) {
	tm := NewToastModel(40, time.Second)
	in := "first line\nsecond line\nthird line"
	if got := tm.Render(in); got != in {
		t.Errorf("inactive toast must passthrough; got %q", got)
	}
}

func TestToast_ShowActivatesAndUpdateConsumes(t *testing.T) {
	tm := NewToastModel(40, time.Second)
	cmd := tm.show("hi there")
	if cmd == nil {
		t.Fatal("show should return a cmd")
	}
	msg := cmd()
	tm2, _ := tm.Update(msg)
	if !tm2.hasActive() {
		t.Errorf("toast should be active after consuming show msg")
	}
	if tm2.text != "hi there" {
		t.Errorf("toast text=%q want hi there", tm2.text)
	}
}

func TestToastRender_ActiveOverlaysOnTopRight(t *testing.T) {
	// Toast renders as a bordered chip (3 rows: top border, body,
	// bottom border). Feed it more rows than that so we can assert
	// the lines BELOW the chip are completely untouched.
	tm := NewToastModel(20, time.Second)
	tm.active = true
	tm.text = "ok"
	tm.expires = time.Now().Add(time.Second)
	const rows = 6
	row := strings.Repeat("x", 60)
	in := strings.Repeat(row+"\n", rows-1) + row
	out := tm.Render(in)
	lines := strings.Split(out, "\n")
	if len(lines) != rows {
		t.Fatalf("line count changed by Render: got %d, want %d", len(lines), rows)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("output should contain toast text, got %q", out)
	}
	// The chip is 3 rows tall — rows below row 2 (0-indexed) should
	// be the original line, byte-for-byte.
	for i := 3; i < rows; i++ {
		if lines[i] != row {
			t.Errorf("line %d below the chip was modified: got %q want %q", i, lines[i], row)
		}
	}
	// And the top three lines should each have been modified by the
	// overlay (chip text appended at right).
	for i := 0; i < 3; i++ {
		if lines[i] == row {
			t.Errorf("line %d should carry the toast overlay, but matches the input untouched", i)
		}
	}
}

func TestToast_AutoDismissAfterDuration(t *testing.T) {
	tm := NewToastModel(40, 50*time.Millisecond)
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	tm.clock = func() time.Time { return now }
	cmd := tm.show("bye")
	tm2, _ := tm.Update(cmd())
	if !tm2.hasActive() {
		t.Fatal("expected toast active right after show")
	}
	// First tick: still within duration.
	tm3, tickCmd := tm2.Update(toastTickMsg{})
	if !tm3.hasActive() {
		t.Errorf("toast should still be active before duration elapses")
	}
	if tickCmd == nil {
		t.Errorf("active toast should keep ticking")
	}
	// Advance the clock past expiry.
	now = now.Add(time.Second)
	tm3.clock = func() time.Time { return now }
	tm4, doneCmd := tm3.Update(toastTickMsg{})
	if tm4.hasActive() {
		t.Errorf("toast should auto-dismiss once expired")
	}
	if doneCmd != nil {
		t.Errorf("expired toast should stop scheduling ticks")
	}
}

func TestToast_TickWithoutActiveIsHarmless(t *testing.T) {
	tm := NewToastModel(40, time.Second)
	tm2, cmd := tm.Update(toastTickMsg{})
	if tm2.hasActive() {
		t.Errorf("tick should not activate a dormant toast")
	}
	if cmd != nil {
		t.Errorf("tick on dormant toast should not re-arm")
	}
}

func TestToast_ApplyThemeRebuildsStyle(t *testing.T) {
	tm := NewToastModel(40, time.Second)
	pre := tm.style
	tm.applyTheme(activeTheme)
	if tm.style.GetBold() != true {
		t.Errorf("themed toast should be bold")
	}
	_ = pre // ensure compile when theme is no-op
}
