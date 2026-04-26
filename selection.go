package main

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// cellPos points at a cell in the *content* coordinate system of the
// viewport: row counts from the top of the rendered content (not the
// screen), col counts from the start of the line (not from inside the
// viewport's right-margin scrollbar). Storing in content coords lets the
// selection survive scrolling and viewport resizes.
type cellPos struct {
	row, col int
}

// userBarMarginCols is the first column where actual user-message text
// lives. userBarStyle wraps the message with MarginLeft(3) +
// left-border + PaddingLeft(1), which lays out as:
//
//	col 0..2 : MarginLeft spaces
//	col 3    : │ border character
//	col 4    : PaddingLeft space
//	col 5+   : text
//
// The selection renderer suppresses highlight on cols < this value so
// the | bar, the indentation, and the padding gap never appear
// highlighted (acceptance criterion 1).
const userBarMarginCols = 5

// selectionBounds is the normalized rectangle for a live or finalized
// selection, expressed as inclusive content cells.
type selectionBounds struct {
	minRow, minCol, maxRow, maxCol int
}

// selectionRange returns the normalized inclusive bounds of the current
// selection. ok=false means there is no selection (anchor == focus or
// neither dragging nor active). Callers must check ok before reading
// bounds — the rectangle struct is zero when ok is false.
func (m model) selectionRange() (selectionBounds, bool) {
	if !m.selDragging && !m.selActive {
		return selectionBounds{}, false
	}
	if m.selAnchor == m.selFocus {
		return selectionBounds{}, false
	}
	a, b := m.selAnchor, m.selFocus
	if a.row > b.row || (a.row == b.row && a.col > b.col) {
		a, b = b, a
	}
	return selectionBounds{
		minRow: a.row, minCol: a.col,
		maxRow: b.row, maxCol: b.col,
	}, true
}

// selectionContains reports whether (row, col) sits inside the selection
// rectangle using terminal block-selection semantics: on the first row
// only col >= minCol counts, on the last row only col <= maxCol counts,
// and middle rows include every column. Returns false when there is no
// active selection so renderers can short-circuit.
func (m model) selectionContains(row, col int) bool {
	b, ok := m.selectionRange()
	if !ok {
		return false
	}
	if row < b.minRow || row > b.maxRow {
		return false
	}
	if b.minRow == b.maxRow {
		return col >= b.minCol && col <= b.maxCol
	}
	switch row {
	case b.minRow:
		return col >= b.minCol
	case b.maxRow:
		return col <= b.maxCol
	default:
		return true
	}
}

// entryRowRanges returns, for each history entry, the [start, end)
// content-row range it occupies in the rendered viewport content. Used
// to map a selection (in content rows) back to history entries when
// building the clipboard payload.
//
// The viewport content is rebuilt by viewportContent() which joins
// entries with "\n\n", so each entry contributes lipgloss.Height(rendered)
// rows followed by exactly one blank separator row (except the last
// entry). entryRowRanges mirrors that layout.
func (m model) entryRowRanges() [][2]int {
	out := make([][2]int, len(m.history))
	row := 0
	for i := range m.history {
		var rendered string
		switch m.history[i].kind {
		case histResponse, histUser:
			rendered = m.history[i].rendered
			if rendered == "" {
				// renderEntry never returns empty for these kinds
				// when the lazy render has run, but we still need to
				// produce *some* count for unrendered rows so a copy
				// before first View() doesn't crash.
				rendered = m.history[i].text
			}
		default:
			rendered = m.history[i].text
		}
		h := max(1, lipgloss.Height(rendered))
		out[i] = [2]int{row, row + h}
		row += h + 1 // +1 for the "\n\n" separator
	}
	return out
}

// buildCopyText assembles the clipboard payload for the current
// selection: every history entry whose row range intersects the
// selection contributes its full e.text (the buffer), joined by
// "\n\n". This deliberately ignores partial intra-entry selections —
// using the buffer guarantees no soft-wrap newlines leak in and
// satisfies the "respect buffer newlines" acceptance criterion.
func (m model) buildCopyText() string {
	b, ok := m.selectionRange()
	if !ok {
		return ""
	}
	ranges := m.entryRowRanges()
	var parts []string
	for i, r := range ranges {
		if r[1] <= b.minRow || r[0] > b.maxRow {
			continue
		}
		parts = append(parts, m.history[i].text)
	}
	return strings.Join(parts, "\n\n")
}

// clearSelection drops both the live drag and any finalized selection
// so the next render skips the highlight pass. Always called via a
// pointer receiver so the field updates propagate.
func (m *model) clearSelection() {
	m.selDragging = false
	m.selActive = false
	m.selAnchor = cellPos{}
	m.selFocus = cellPos{}
}

// copySelectionAndClear is the right-click handler entry point: it
// builds the buffer-text payload from the current selection, kicks off
// an async clipboard write, and clears the selection synchronously so
// the highlight disappears immediately. The returned tea.Cmd carries
// the clipboard write and the resulting toast (success or failure).
//
// Right-clicking with no active selection is a no-op (no toast, no
// clipboard call); the caller in update.go gates on selActive.
func (m model) copySelectionAndClear() (tea.Model, tea.Cmd) {
	text := m.buildCopyText()
	(&m).clearSelection()
	m.lastContentFP = ""
	if text == "" {
		return m, nil
	}
	cmd := copyTextCmd(m.toast, text)
	return m, cmd
}

// copyTextCmd writes text to the OS clipboard off the main update
// goroutine, then dispatches a toast trigger. Wrapping in a tea.Cmd
// (instead of doing it synchronously in Update) means a slow or stuck
// pbcopy/wl-copy never blocks the UI thread.
func copyTextCmd(t *toastModel, text string) tea.Cmd {
	if t == nil {
		return nil
	}
	return func() tea.Msg {
		if err := clipboardCopyText(text); err != nil {
			return toastShowMsg{text: "copy failed: " + err.Error()}
		}
		return toastShowMsg{text: "copied to clipboard"}
	}
}

// entryKindAt reads the history-entry kind covering contentRow off a
// precomputed ranges slice. Returns histPrerendered when the row is
// past the end of history (trailing blanks in a partially filled
// viewport).
func entryKindAt(history []historyEntry, ranges [][2]int, contentRow int) historyKind {
	for i, r := range ranges {
		if contentRow >= r[0] && contentRow < r[1] {
			return history[i].kind
		}
	}
	return histPrerendered
}

// selectionRenderMask returns the inclusive-start / exclusive-end
// column range to paint with the selection background on a given
// content row. The mask handles three concerns in one place so the
// renderer (and tests) get a single source of truth:
//
//   - terminal block-selection semantics (first row from minCol, last
//     row up to maxCol, middle rows full width)
//   - user-bar margin suppression — cols 0..userBarMarginCols-1 on
//     histUser rows never get highlighted so the | bar and its left
//     margin stay clean
//   - clamping to the actual line width so the highlight never extends
//     into trailing blank cells
//
// ranges may be nil; the helper falls back to entryRowRanges() then.
// Pass a precomputed slice from applySelectionHighlight to avoid
// re-walking history on every line.
//
// ok=false means there's nothing to paint on this row (no selection,
// row outside selection range, or fully clamped by the user-bar margin).
func (m model) selectionRenderMask(contentRow, lineWidth int, ranges [][2]int) (start, end int, ok bool) {
	b, hasRange := m.selectionRange()
	if !hasRange {
		return 0, 0, false
	}
	if contentRow < b.minRow || contentRow > b.maxRow {
		return 0, 0, false
	}
	switch {
	case b.minRow == b.maxRow:
		start = b.minCol
		end = b.maxCol + 1
	case contentRow == b.minRow:
		start = b.minCol
		end = lineWidth
	case contentRow == b.maxRow:
		start = 0
		end = b.maxCol + 1
	default:
		start = 0
		end = lineWidth
	}
	end = min(end, lineWidth)
	if end <= start {
		return 0, 0, false
	}
	if ranges == nil {
		ranges = m.entryRowRanges()
	}
	if entryKindAt(m.history, ranges, contentRow) == histUser {
		start = max(start, userBarMarginCols)
	}
	if end <= start {
		return 0, 0, false
	}
	return start, end, true
}

// selectionStyle is the lipgloss style used to paint highlighted cells
// on screen. Falls back to reverse-video when the active theme has no
// row-highlight color (default theme), so the selection still reads on
// terminals without 256-color support.
func selectionStyle() lipgloss.Style {
	if activeTheme.rowHL == nil {
		return lipgloss.NewStyle().Reverse(true)
	}
	return lipgloss.NewStyle().Background(activeTheme.rowHL)
}

// selectionFingerprint is the stable string representation of the
// current selection used in viewport cache keys. Empty when there's no
// selection so the no-selection cache path stays effective. Drag and
// active selections render identically today, so the fingerprint keys
// only on bounds — adding a flag would cause unnecessary cache misses
// every time a drag finalizes.
func (m model) selectionFingerprint() string {
	b, ok := m.selectionRange()
	if !ok {
		return ""
	}
	return strconv.Itoa(b.minRow) + "," + strconv.Itoa(b.minCol) + "-" +
		strconv.Itoa(b.maxRow) + "," + strconv.Itoa(b.maxCol)
}

// screenToContentCell converts a screen-space mouse coordinate (the X/Y
// from a tea.Mouse* event) into a content-space cellPos suitable for
// selAnchor / selFocus. The viewport sits at (0,0) of the screen, but
// its outer Style applies a top frame (PaddingTop(1) in production), so
// the first content row lives at screenY = frameTop. Clicks landing on
// the padding band itself collapse onto the topmost visible content row
// rather than scrolling negative. Caller is responsible for confirming
// the click is inside the viewport's screen footprint before calling.
func (m model) screenToContentCell(screenX, screenY int) cellPos {
	frameTop := m.viewport.Style.GetPaddingTop() +
		m.viewport.Style.GetMarginTop() +
		m.viewport.Style.GetBorderTopSize()
	contentY := max(0, screenY-frameTop)
	return cellPos{row: contentY + m.viewport.YOffset(), col: screenX}
}
