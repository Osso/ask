package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestSelectionRange_NoSelection(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	if _, ok := m.selectionRange(); ok {
		t.Fatal("zero-state model should have no selection range")
	}
}

func TestSelectionRange_ZeroLengthIsNotASelection(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selDragging = true
	m.selAnchor = cellPos{row: 5, col: 7}
	m.selFocus = cellPos{row: 5, col: 7}
	if _, ok := m.selectionRange(); ok {
		t.Fatal("anchor==focus should not produce a range — caller would copy nothing useful")
	}
}

func TestSelectionRange_NormalizesBackwardsDrag(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selActive = true
	m.selAnchor = cellPos{row: 12, col: 5}
	m.selFocus = cellPos{row: 4, col: 9}
	b, ok := m.selectionRange()
	if !ok {
		t.Fatal("expected an active range")
	}
	if b.minRow != 4 || b.maxRow != 12 {
		t.Errorf("rows not normalized: %+v", b)
	}
	if b.minCol != 9 || b.maxCol != 5 {
		t.Errorf("cols not preserved per row anchor (we sort by row first): %+v", b)
	}
}

func TestSelectionContains_SingleRow(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selActive = true
	m.selAnchor = cellPos{row: 3, col: 5}
	m.selFocus = cellPos{row: 3, col: 12}
	cases := []struct {
		row, col int
		want     bool
	}{
		{3, 4, false},   // before
		{3, 5, true},    // start (inclusive)
		{3, 8, true},    // middle
		{3, 12, true},   // end (inclusive)
		{3, 13, false},  // after
		{2, 8, false},   // wrong row
		{4, 8, false},   // wrong row
	}
	for _, c := range cases {
		if got := m.selectionContains(c.row, c.col); got != c.want {
			t.Errorf("selectionContains(%d,%d) = %v, want %v", c.row, c.col, got, c.want)
		}
	}
}

func TestSelectionContains_MultiRowBlock(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selActive = true
	m.selAnchor = cellPos{row: 2, col: 6}
	m.selFocus = cellPos{row: 4, col: 3}
	// First row: only col >= 6
	if !m.selectionContains(2, 6) || !m.selectionContains(2, 99) {
		t.Errorf("first row should include col >= minCol (terminal block selection)")
	}
	if m.selectionContains(2, 5) {
		t.Errorf("first row should exclude col < minCol")
	}
	// Middle row: every column.
	if !m.selectionContains(3, 0) || !m.selectionContains(3, 1000) {
		t.Errorf("middle row should include every col")
	}
	// Last row: only col <= 3.
	if !m.selectionContains(4, 0) || !m.selectionContains(4, 3) {
		t.Errorf("last row should include col <= maxCol")
	}
	if m.selectionContains(4, 4) {
		t.Errorf("last row should exclude col > maxCol")
	}
}

func TestEntryRowRanges_TracksHeightsWithSeparator(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histUser, text: "hi", rendered: "▎ hi"},
		{kind: histResponse, text: "1\n2\n3", rendered: "1\n2\n3"},
		{kind: histPrerendered, text: "single"},
	}
	got := m.entryRowRanges()
	want := [][2]int{
		{0, 1},  // 1-row user, then separator at row 1
		{2, 5},  // 3-row response (rows 2,3,4), separator at row 5
		{6, 7},  // 1-row prerendered
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch got=%v want=%v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("range[%d] = %v want %v", i, got[i], want[i])
		}
	}
}

func TestBuildCopyText_FullEntrySelection(t *testing.T) {
	// Selection across two entries (and the blank separator between
	// them) copies the visible glyphs of both entries verbatim, with a
	// blank line preserving the on-screen separator. Production
	// outputStyle bakes a 5-col MarginLeft into rendered text, so the
	// fixture mirrors that layout.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histResponse, text: "first", rendered: "     first"},
		{kind: histResponse, text: "second", rendered: "     second"},
	}
	// Ranges: entry0 [0,1), separator at row 1, entry1 [2,3).
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 2, col: 10}
	got := m.buildCopyText()
	want := "first\n\nsecond"
	if got != want {
		t.Errorf("buildCopyText:\n got %q\nwant %q", got, want)
	}
}

func TestBuildCopyText_PartialSelectionCopiesOnlySelectedGlyphs(t *testing.T) {
	// Acceptance: a partial intra-entry selection copies only the
	// glyphs the user actually highlighted on screen, not the full
	// buffer. Inter-row middles span the entire wrapped line per
	// terminal block-selection semantics.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{
			kind:       histResponse,
			text:       "raw markdown source",
			rendered:   "     wrap1\n     wrap2\n     wrap3\n     wrap4",
			wrapped:    []string{"     wrap1", "     wrap2", "     wrap3", "     wrap4"},
			wrappedFor: 80,
		},
	}
	// Select from row 1 col 7 to row 2 col 8. First row from col 7
	// (post-margin "ap2") to end of line, last row from col 5
	// (post-margin) to col 8 inclusive ("wrap").
	m.selActive = true
	m.selAnchor = cellPos{row: 1, col: 7}
	m.selFocus = cellPos{row: 2, col: 8}
	got := m.buildCopyText()
	want := "ap2\nwrap"
	if got != want {
		t.Errorf("partial selection should copy only selected glyphs:\n got %q\nwant %q", got, want)
	}
}

func TestBuildCopyText_OnlySelectedEntries(t *testing.T) {
	// Selection inside a single entry copies only that entry's visible
	// text. Three entries; user selects all of the middle entry's row.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histResponse, text: "first", rendered: "     first"},
		{kind: histResponse, text: "second", rendered: "     second"},
		{kind: histResponse, text: "third", rendered: "     third"},
	}
	// Each entry: 1 row of content + 1 separator.
	// Rows: first=[0,1), sep=1, second=[2,3), sep=3, third=[4,5).
	m.selActive = true
	m.selAnchor = cellPos{row: 2, col: 0}
	m.selFocus = cellPos{row: 2, col: 10}
	got := m.buildCopyText()
	if got != "second" {
		t.Errorf("expected middle entry's visible text, got %q", got)
	}
}

func TestBuildCopyText_StripsAnsiEscapes(t *testing.T) {
	// Acceptance: ANSI escapes from glamour styling never leak into
	// the clipboard payload — the output is the bare visible text.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{
			kind:       histResponse,
			text:       "**hello**",
			rendered:   "     \x1b[1mhello\x1b[0m",
			wrapped:    []string{"     \x1b[1mhello\x1b[0m"},
			wrappedFor: 80,
		},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 0, col: 9}
	got := m.buildCopyText()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("copy text must not contain ANSI escapes; got %q", got)
	}
	if got != "hello" {
		t.Errorf("got %q, want \"hello\"", got)
	}
}

func TestBuildCopyText_LeftMarginSkippedAcrossEntryKinds(t *testing.T) {
	// Acceptance: the 5-column left gutter (outputStyle.MarginLeft for
	// histResponse / histPrerendered, the indent + │ + padding for the
	// userBar) is never part of the clipboard payload regardless of
	// entry kind.
	cases := []struct {
		name     string
		kind     historyKind
		rendered string
		anchor   cellPos
		focus    cellPos
		want     string
	}{
		{
			name:     "histUser bar gutter",
			kind:     histUser,
			rendered: "   │ hello world",
			anchor:   cellPos{row: 0, col: 0},
			focus:    cellPos{row: 0, col: 15},
			want:     "hello world",
		},
		{
			name:     "histResponse left margin",
			kind:     histResponse,
			rendered: "     response body",
			anchor:   cellPos{row: 0, col: 0},
			focus:    cellPos{row: 0, col: 17},
			want:     "response body",
		},
		{
			name:     "histPrerendered left margin",
			kind:     histPrerendered,
			rendered: "     tool output line",
			anchor:   cellPos{row: 0, col: 0},
			focus:    cellPos{row: 0, col: 20},
			want:     "tool output line",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t, newFakeProvider())
			m.history = []historyEntry{
				{
					kind:       c.kind,
					text:       c.want,
					rendered:   c.rendered,
					wrapped:    []string{c.rendered},
					wrappedFor: 80,
				},
			}
			m.selActive = true
			m.selAnchor = c.anchor
			m.selFocus = c.focus
			if got := m.buildCopyText(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestClearSelection_ResetsAllFields(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selDragging = true
	m.selActive = true
	m.selAnchor = cellPos{row: 1, col: 2}
	m.selFocus = cellPos{row: 3, col: 4}
	m.clearSelection()
	if m.selDragging || m.selActive {
		t.Errorf("flags not cleared: dragging=%v active=%v", m.selDragging, m.selActive)
	}
	if (m.selAnchor != cellPos{}) || (m.selFocus != cellPos{}) {
		t.Errorf("positions not zeroed: anchor=%+v focus=%+v", m.selAnchor, m.selFocus)
	}
}

func TestSelectionRenderMask_UserBarMarginNeverHighlighted(t *testing.T) {
	// Acceptance criterion (1): the | indent / left margin of a user
	// entry must never be highlighted, even when the selection range
	// covers cols 0..n on that row.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histUser, text: "user", rendered: "▎ user message"},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 0, col: 12}
	start, end, ok := m.selectionRenderMask(0, 14, nil)
	if !ok {
		t.Fatal("expected mask on user-bar row to clamp, not vanish")
	}
	if start != chatLeftMarginCols {
		t.Errorf("start=%d want %d (user-bar margin must be skipped)", start, chatLeftMarginCols)
	}
	if end != 13 {
		t.Errorf("end=%d want 13 (maxCol+1)", end)
	}
}

func TestSelectionRenderMask_UserBarFullyClampedReturnsFalse(t *testing.T) {
	// Selection that lives entirely inside the user-bar margin should
	// produce no mask at all, so the renderer doesn't paint a 0-width
	// highlight that flickers as a single cell.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histUser, text: "u", rendered: "▎ u"},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 0, col: 3}
	if _, _, ok := m.selectionRenderMask(0, 10, nil); ok {
		t.Errorf("selection covering only cols 0..3 on histUser row should yield ok=false")
	}
}

func TestSelectionRenderMask_AllRowsClampToLeftMargin(t *testing.T) {
	// All entry kinds share a 5-col left gutter, so the highlight mask
	// always starts at chatLeftMarginCols regardless of kind.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histResponse, text: "resp", rendered: "     response text"},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 0, col: 17}
	start, end, ok := m.selectionRenderMask(0, 18, nil)
	if !ok {
		t.Fatal("expected a mask")
	}
	if start != chatLeftMarginCols {
		t.Errorf("non-user rows must clamp to chatLeftMarginCols; got start=%d", start)
	}
	if end != 18 {
		t.Errorf("end=%d want 18 (maxCol+1)", end)
	}
}

func TestSelectionRenderMask_MultiRowMiddleRowSpansPostMargin(t *testing.T) {
	// Middle rows of a multi-row selection span [chatLeftMarginCols,
	// lineWidth) — left margin is always reserved.
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histResponse, text: "x", rendered: "     row0\n     row1\n     row2"},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 2}
	m.selFocus = cellPos{row: 2, col: 6}
	start, end, ok := m.selectionRenderMask(1, 9, nil)
	if !ok {
		t.Fatal("middle row should be selected")
	}
	if start != chatLeftMarginCols || end != 9 {
		t.Errorf("middle row mask=[%d,%d) want [%d,9)", start, end, chatLeftMarginCols)
	}
}

func TestApplySelectionHighlight_NoSelectionReturnsInputUnchanged(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	in := "line one\nline two"
	if got := m.applySelectionHighlight(in); got != in {
		t.Errorf("no-selection path must be a passthrough; got %q want %q", got, in)
	}
}

func TestApplySelectionHighlight_AddsAnsiOnSelectedCells(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.history = []historyEntry{
		{kind: histResponse, text: "hello world", rendered: "     hello world"},
	}
	// Select cols 5..10 (post-margin "hello") so the mask isn't fully
	// clamped away.
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 5}
	m.selFocus = cellPos{row: 0, col: 9}
	out := m.applySelectionHighlight("     hello world")
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI escape in highlighted output, got %q", out)
	}
	if out == "     hello world" {
		t.Errorf("highlighted output should differ from input")
	}
}

func TestUpdateMouseLeftClick_StartsSelection(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.chat.SetWidth(80)
	m.chat.SetHeight(20)
	m2, _ := runUpdate(t, m, tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	if !m2.selDragging {
		t.Errorf("left click in viewport should set selDragging")
	}
	if m2.selAnchor.col != 10 {
		t.Errorf("anchor col=%d want 10", m2.selAnchor.col)
	}
	if m2.selFocus != m2.selAnchor {
		t.Errorf("anchor and focus should match on initial click")
	}
}

func TestUpdateMouseLeftClick_ScrollbarUnaffected(t *testing.T) {
	// Acceptance criterion: scrollbar drag must keep working — left
	// click on the rightmost column with content longer than viewport
	// height starts scrollbarDragging and never starts a text selection.
	m := newTestModel(t, newFakeProvider())
	m.width = 40
	m.chat.SetWidth(39)
	m.chat.SetHeight(5)
	for i := 0; i < 100; i++ {
		m.appendHistory("line " + strconv.Itoa(i))
	}
	(&m).layout()
	msg := tea.MouseClickMsg{X: m.width - 1, Y: 2, Button: tea.MouseLeft}
	m2, _ := runUpdate(t, m, msg)
	if !m2.scrollbarDragging {
		t.Errorf("scrollbar click should set scrollbarDragging")
	}
	if m2.selDragging {
		t.Errorf("scrollbar click must not start a text selection")
	}
}

func TestUpdateMouseMotion_UpdatesSelectionFocus(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.width = 80
	m.chat.SetWidth(80)
	m.chat.SetHeight(20)
	m.selDragging = true
	m.selAnchor = cellPos{row: 1, col: 3}
	m.selFocus = cellPos{row: 1, col: 3}
	m2, _ := runUpdate(t, m, tea.MouseMotionMsg{X: 15, Y: 8})
	if m2.selFocus.col != 15 {
		t.Errorf("motion col=%d want 15", m2.selFocus.col)
	}
}

func TestUpdateMouseRelease_FinalizesSelection(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.width = 80
	m.selDragging = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 0, col: 5}
	m2, _ := runUpdate(t, m, tea.MouseReleaseMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	if m2.selDragging {
		t.Errorf("release must clear selDragging")
	}
	if !m2.selActive {
		t.Errorf("non-degenerate release should activate selection")
	}
}

func TestUpdateMouseRelease_DegenerateSelectionClears(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selDragging = true
	m.selAnchor = cellPos{row: 0, col: 5}
	m.selFocus = cellPos{row: 0, col: 5}
	m2, _ := runUpdate(t, m, tea.MouseReleaseMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	if m2.selDragging || m2.selActive {
		t.Errorf("anchor==focus release should clear, not finalize")
	}
}

func TestUpdateMouseRightClick_NoSelectionIsNoOp(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m2, cmd := runUpdate(t, m, tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseRight})
	if cmd != nil {
		t.Errorf("right-click without selection must not return a cmd")
	}
	if m2.selActive {
		t.Errorf("no selection should remain after no-op right-click")
	}
}

func TestUpdateMouseRightClick_WithSelectionCopiesAndClears(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.toast = NewToastModel(40, time.Second)
	m.history = []historyEntry{
		{kind: histResponse, text: "buffer source", rendered: "     buffer source"},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 0}
	m.selFocus = cellPos{row: 0, col: 10}

	var copied string
	withClipboardStubs(t, "linux",
		map[string]bool{"wl-copy": true},
		func(name, stdin string, args ...string) error {
			copied = stdin
			return nil
		})

	m2, cmd := runUpdate(t, m, tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseRight})
	if m2.selActive || m2.selDragging {
		t.Errorf("right-click must clear selection synchronously")
	}
	if cmd == nil {
		t.Fatal("expected a copy/toast cmd")
	}
	msg := cmd()
	tmsg, ok := msg.(toastShowMsg)
	if !ok {
		t.Fatalf("expected toastShowMsg, got %T", msg)
	}
	// Cols 0..10 of "     buffer source", clamped to [5,11) = "buffer".
	if copied != "buffer" {
		t.Errorf("clipboard payload=%q want \"buffer\" (post-margin cols 5..10)", copied)
	}
	if !strings.Contains(tmsg.text, "copied") {
		t.Errorf("toast text=%q should announce success", tmsg.text)
	}
}

func TestCopySelectionAndClear_ClipboardErrorSurfacesToast(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.toast = NewToastModel(40, time.Second)
	m.history = []historyEntry{
		{kind: histResponse, text: "xy", rendered: "     xy"},
	}
	m.selActive = true
	m.selAnchor = cellPos{row: 0, col: 5}
	m.selFocus = cellPos{row: 0, col: 6}

	withClipboardStubs(t, "linux",
		map[string]bool{"wl-copy": true},
		func(name, stdin string, args ...string) error {
			return errors.New("clipboard daemon offline")
		})
	_, cmd := m.copySelectionAndClear()
	if cmd == nil {
		t.Fatal("expected toast cmd even on error")
	}
	tmsg, ok := cmd().(toastShowMsg)
	if !ok {
		t.Fatalf("expected toastShowMsg, got %T", cmd())
	}
	if !strings.Contains(tmsg.text, "copy failed") {
		t.Errorf("error toast=%q should include 'copy failed'", tmsg.text)
	}
}

func TestSelectionFingerprint_EmptyWhenNoSelection(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	if got := m.selectionFingerprint(); got != "" {
		t.Errorf("expected empty fp, got %q", got)
	}
}

func TestSelectionFingerprint_ChangesWithBounds(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.selActive = true
	m.selAnchor = cellPos{row: 1, col: 2}
	m.selFocus = cellPos{row: 3, col: 4}
	first := m.selectionFingerprint()
	if first == "" {
		t.Fatal("active selection should produce non-empty fingerprint")
	}
	m.selFocus = cellPos{row: 3, col: 5}
	if next := m.selectionFingerprint(); next == first {
		t.Errorf("changing the focus must change the cache fingerprint; both = %q", first)
	}
}

func TestScreenToContentCell_AddsViewportYOffset(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	// Push enough content through the chat view to allow a non-zero
	// scroll offset, then move it explicitly.
	m.chat.SetWidth(40)
	m.chat.SetHeight(5)
	for i := 0; i < 100; i++ {
		m.appendHistory("line " + strconv.Itoa(i))
	}
	(&m).layout()
	m.chat.SetYOffset(7)
	cell := m.screenToContentCell(3, 2)
	if cell.col != 3 {
		t.Errorf("col passes through unchanged, got %d", cell.col)
	}
	wantRow := 2 + m.chat.YOffset() - m.chat.style.GetPaddingTop() -
		m.chat.style.GetMarginTop() - m.chat.style.GetBorderTopSize()
	if wantRow < m.chat.YOffset() {
		// frame top would push contentY below 0; clamp matches the
		// behaviour of screenToContentCell's max(0, ...) guard.
		wantRow = m.chat.YOffset()
	}
	if cell.row != wantRow {
		t.Errorf("row should be screenY+YOffset less frame top = %d, got %d", wantRow, cell.row)
	}
}
