package main

import (
	"strings"
	"testing"
)

func TestProviderChip_ShowsIDAndModel(t *testing.T) {
	p := newFakeProvider()
	p.id = "claude"
	m := newTestModel(t, p)
	m.providerModel = "sonnet"
	got := m.providerChip()
	if !strings.Contains(got, "p: claude") {
		t.Errorf("chip should contain 'p: claude', got %q", got)
	}
	if !strings.Contains(got, "m: sonnet") {
		t.Errorf("chip should contain 'm: sonnet', got %q", got)
	}
}

func TestProviderChip_DefaultModelLabel(t *testing.T) {
	p := newFakeProvider()
	p.id = "codex"
	m := newTestModel(t, p)
	m.providerModel = ""
	got := m.providerChip()
	if !strings.Contains(got, "m: default") {
		t.Errorf("empty providerModel should render as 'm: default', got %q", got)
	}
}

func TestProviderChip_NilProviderEmpty(t *testing.T) {
	var m model
	if got := m.providerChip(); got != "" {
		t.Errorf("nil provider chip must be empty, got %q", got)
	}
}

func TestStatusChipRow_RightAnchorsProviderChip(t *testing.T) {
	p := newFakeProvider()
	p.id = "claude"
	m := newTestModel(t, p)
	m.width = 80
	row := m.statusChipRow()
	if row == "" {
		t.Fatal("statusChipRow should render the provider chip even with no left content")
	}
	// The row's rendered width must fit inside (width - 2) so the
	// scrollbar column isn't clobbered.
	if w := visibleWidth(row); w > m.width-1 {
		t.Errorf("status chip row width=%d exceeds width-1=%d: %q", w, m.width-1, row)
	}
}

func TestStatusChipRow_LeftAndRightCoexist(t *testing.T) {
	p := newFakeProvider()
	p.id = "claude"
	m := newTestModel(t, p)
	m.width = 100
	m.worktreeName = "feat-x"
	row := m.statusChipRow()
	if !strings.Contains(row, "feat-x") {
		t.Errorf("left worktree chip should render: %q", row)
	}
	if !strings.Contains(row, "p: claude") {
		t.Errorf("right provider chip should render: %q", row)
	}
	// Right chip must appear after the left chip.
	leftAt := strings.Index(row, "feat-x")
	rightAt := strings.Index(row, "p: claude")
	if leftAt < 0 || rightAt < 0 || leftAt >= rightAt {
		t.Errorf("right chip must follow left chip: leftAt=%d rightAt=%d row=%q", leftAt, rightAt, row)
	}
}

func TestStatusChipHeight_OneWhenRendered(t *testing.T) {
	p := newFakeProvider()
	m := newTestModel(t, p)
	m.width = 80
	if h := m.statusChipHeight(); h != 1 {
		t.Errorf("statusChipHeight should be 1 when a chip renders, got %d", h)
	}
}

func TestStatusChipHeight_ZeroWhenNothingToShow(t *testing.T) {
	// No provider, no width, no worktree — row is empty.
	var m model
	if h := m.statusChipHeight(); h != 0 {
		t.Errorf("statusChipHeight with nothing to show=%d want 0", h)
	}
}

// visibleWidth is a lightweight visible-width count that strips ANSI
// escape sequences — we can't assert exact bytes because lipgloss adds
// styling. The string is "content + escape codes", and since tests use
// plain input and our chip functions style with dimStyle, we compare
// against the structure only. Use lipgloss.Width via lipgloss pkg for
// a real terminal width.
func visibleWidth(s string) int {
	// Strip ESC...m sequences.
	out := 0
	inEsc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// skip
		default:
			out++
		}
	}
	return out
}
