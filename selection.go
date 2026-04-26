package main

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// cellPos points at a cell in the *content* coordinate system of the
// viewport: row counts from the top of the rendered content (not the
// screen), col counts from the start of the line (not from inside the
// viewport's right-margin scrollbar). Storing in content coords lets the
// selection survive scrolling and viewport resizes.
type cellPos struct {
	row, col int
}

// chatLeftMarginCols is the first column where actual entry content
// lives. Every entry kind shares the same 5-column left margin:
//
//   - userBarStyle: MarginLeft(3) + left border + PaddingLeft(1) →
//     cols 0..2 are spaces, col 3 is the │ border, col 4 is the
//     padding space, col 5+ is the user text.
//   - outputStyle (every other kind: histResponse, histPrerendered):
//     MarginLeft(5) → cols 0..4 are spaces, col 5+ is the text.
//
// The selection renderer suppresses highlight (and copy) on cols
// < this value so the indent / bar / padding gutter never appears
// highlighted, regardless of entry kind.
const chatLeftMarginCols = 5

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
// The chatView lays out entries by joining wrapped lines with one
// blank separator row between consecutive entries; entryRowRanges
// mirrors that layout. It prefers the per-entry wrap cache (exact for
// the current width) and falls back to lipgloss.Height(rendered) for
// entries that have never been wrapped — so a copy issued before the
// first View() lands still produces correct ranges, just at the
// pre-wrap line count.
func (m model) entryRowRanges() [][2]int {
	out := make([][2]int, len(m.history))
	row := 0
	for i := range m.history {
		h := 1
		switch {
		case m.history[i].wrapped != nil:
			h = max(1, len(m.history[i].wrapped))
		default:
			rendered := m.history[i].rendered
			if rendered == "" {
				rendered = m.history[i].text
			}
			h = max(1, lipgloss.Height(rendered))
		}
		out[i] = [2]int{row, row + h}
		row += h + 1 // +1 for the blank separator row
	}
	return out
}

// buildCopyText assembles the clipboard payload for the current
// selection: it walks every selected content row, slices each row to
// the selected column range using selectionRenderMask (which knows
// about user-bar margins and terminal block-selection rules), strips
// ANSI escapes, and joins the rows with newlines. The output matches
// what the user actually highlighted on screen — partial intra-entry
// selections only copy the selected glyphs.
//
// Separator rows between entries copy as empty lines, preserving the
// blank-line gap the user sees on screen. Rows that fall outside any
// rendered entry (degenerate selection past the end of history) also
// copy as empty lines so the line count of the payload matches the
// selection rectangle's height.
func (m model) buildCopyText() string {
	b, ok := m.selectionRange()
	if !ok {
		return ""
	}
	ranges := m.entryRowRanges()
	rows := make([]string, 0, b.maxRow-b.minRow+1)
	for r := b.minRow; r <= b.maxRow; r++ {
		line, inEntry := m.lineAtContentRow(r, ranges)
		if !inEntry {
			rows = append(rows, "")
			continue
		}
		start, end, ok := m.selectionRenderMask(r, lipgloss.Width(line), ranges)
		if !ok {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, xansi.Strip(xansi.Cut(line, start, end)))
	}
	return strings.Join(rows, "\n")
}

// lineAtContentRow returns the rendered line at chat-content row r and
// whether the row sits inside a real entry (false for the blank
// separator between consecutive entries, or for rows past the end of
// history). Falls back to entry.rendered/text when the wrap cache is
// not yet populated so the copy path stays correct in test scenarios
// that bypass viewportContent.
func (m model) lineAtContentRow(r int, ranges [][2]int) (string, bool) {
	for i, rr := range ranges {
		if r < rr[0] || r >= rr[1] {
			continue
		}
		j := r - rr[0]
		if w := m.history[i].wrapped; w != nil && j < len(w) {
			return w[j], true
		}
		src := m.history[i].rendered
		if src == "" {
			src = m.history[i].text
		}
		lines := strings.Split(src, "\n")
		if j < len(lines) {
			return lines[j], true
		}
		return "", true
	}
	return "", false
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

// selectionRenderMask returns the inclusive-start / exclusive-end
// column range to paint with the selection background on a given
// content row. The mask handles three concerns in one place so the
// renderer (and tests) get a single source of truth:
//
//   - terminal block-selection semantics (first row from minCol, last
//     row up to maxCol, middle rows full width)
//   - left-margin suppression — cols 0..chatLeftMarginCols-1 never get
//     highlighted (the indent / user-bar gutter is decoration, not
//     content, regardless of entry kind)
//   - clamping to the actual line width so the highlight never extends
//     into trailing blank cells
//
// ranges is unused today (the margin clamp no longer depends on entry
// kind) but kept in the signature so the highlight loop can pass its
// precomputed slice without an extra allocation if entry-aware logic
// ever comes back.
//
// ok=false means there's nothing to paint on this row (no selection,
// row outside selection range, or fully clamped by the left margin).
func (m model) selectionRenderMask(contentRow, lineWidth int, ranges [][2]int) (start, end int, ok bool) {
	_ = ranges
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
	start = max(start, chatLeftMarginCols)
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
	frameTop := m.chat.style.GetPaddingTop() +
		m.chat.style.GetMarginTop() +
		m.chat.style.GetBorderTopSize()
	contentY := max(0, screenY-frameTop)
	return cellPos{row: contentY + m.chat.YOffset(), col: screenX}
}
