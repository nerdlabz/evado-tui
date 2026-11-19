package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// table renders a windowed, scrollable list of rows. Rows are produced by
// the owner so every view can lay out its own columns while sharing
// scrolling, cursor movement and viewport maths.
type table struct {
	cursor int
	offset int
	height int
	items  int
}

func (t *table) setItems(n int) {
	if n < 0 {
		n = 0
	}
	t.items = n
	t.clamp()
}

// setHeight records the viewport height in rows.
func (t *table) setHeight(h int) {
	if h < 1 {
		h = 1
	}
	t.height = h
	t.clamp()
}

// selected is the highlighted row index, or -1 when empty.
func (t *table) selected() int {
	if t.items == 0 {
		return -1
	}
	return t.cursor
}

func (t *table) move(delta int) {
	if t.items == 0 {
		t.cursor = 0
		return
	}
	t.cursor += delta
	t.clamp()
}

func (t *table) top() {
	t.cursor, t.offset = 0, 0
}

func (t *table) bottom() {
	t.cursor = t.items - 1
	t.clamp()
}

func (t *table) pageSize() int {
	if t.height < 2 {
		return 1
	}
	return t.height - 1
}

func (t *table) halfPage() int {
	n := t.pageSize() / 2
	if n < 1 {
		n = 1
	}
	return n
}

func (t *table) pageDown() { t.move(t.pageSize()) }
func (t *table) pageUp()   { t.move(-t.pageSize()) }

// clamp keeps the cursor in range and scrolls the offset so the cursor is
// always visible.
func (t *table) clamp() {
	if t.items <= 0 {
		t.cursor, t.offset = 0, 0
		return
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	if t.cursor > t.items-1 {
		t.cursor = t.items - 1
	}
	if t.height <= 0 {
		t.offset = 0
		return
	}
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+t.height {
		t.offset = t.cursor - t.height + 1
	}
	if maxOff := t.items - t.height; t.offset > maxOff {
		t.offset = maxOff
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

// visible is how many rows are currently in the viewport.
func (t *table) visible() int {
	if t.items == 0 {
		return 0
	}
	n := min(t.items-t.offset, t.height)
	return max(n, 0)
}

// scrollbar returns one marker per viewport row: a block for the thumb,
// a light pipe for the track.
func (t *table) scrollbar() []string {
	if t.height <= 0 || t.items <= t.height {
		return nil
	}
	thumb := max(t.height*t.height/t.items, 1)
	track := max(t.height-thumb, 1)
	pos := 0
	if span := max(t.items-t.height, 1); span > 0 {
		pos = t.offset * track / span
	}
	out := make([]string, t.height)
	for i := range out {
		if i >= pos && i < pos+thumb {
			out[i] = styAccent.Render("┃")
		} else {
			out[i] = styFaint.Render("╎")
		}
	}
	return out
}

// render draws the visible rows using fn, appending a scrollbar column
// when the list overflows. A click on a row goes to click, when there is one.
func (t *table) render(width int, fn func(i int, selected bool) string, click func(i int, double bool) tea.Cmd) string {
	if t.items == 0 {
		return styFaint.Render("(nothing here yet)")
	}
	t.clamp()
	n := t.visible()
	bar := t.scrollbar()
	// A fixed content width keeps the scrollbar column aligned and stops
	// rows from reflowing as the cursor moves.
	content := max(width-1, 8)
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		idx := t.offset + i
		row := fn(idx, idx == t.cursor)
		row = padTo(row, content)
		if click != nil {
			row = clickable(row, func(double bool) tea.Cmd { return click(idx, double) })
		}
		if len(bar) > 0 {
			row += bar[i]
		} else {
			row += " "
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

// padTo pads or truncates a styled line to exactly w display cells.
func padTo(s string, w int) string {
	n := visibleWidth(s)
	switch {
	case n == w:
		return s
	case n < w:
		return s + strings.Repeat(" ", w-n)
	default:
		return truncate(s, w)
	}
}

// overlay composites modal on top of base, centred both ways. Modal lines
// replace the cells they cover, so a dialog always reads clearly against
// whatever was underneath.
func overlay(base, modal string, width, height int) string {
	modalLines := strings.Split(modal, "\n")
	modalW := 0
	for _, l := range modalLines {
		modalW = max(modalW, visibleWidth(l))
	}
	if modalW > width {
		modalW = width
	}
	return overlayAt(base, modal, max((width-modalW)/2, 0), max((height-len(modalLines))/2, 0), modalW)
}

// overlayAt composites modal on top of base with its corner at left, top.
func overlayAt(base, modal string, left, top, modalW int) string {
	baseLines := strings.Split(base, "\n")
	for i, ml := range strings.Split(modal, "\n") {
		row := top + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = spliceLine(baseLines[row], ml, left, modalW)
	}
	return strings.Join(baseLines, "\n")
}

// spliceLine replaces the cells of line from x to x+w with replacement.
func spliceLine(line, replacement string, x, w int) string {
	// A line shorter than x has no cells out there, so pad it or the modal
	// row would slide to the left edge.
	head := padTo(sliceCells(line, 0, x, false), x)
	tail := sliceCells(line, x+w, max(visibleWidth(line), x+w), true)
	return head + padTo(replacement, w) + tail
}

// sliceCells returns the part of a styled line covering cells [from, to).
// The end of a line keeps the zone markers past its last cell, so a region
// a dialog was spliced into still closes; the start of one leaves them out,
// so the region carries on under the dialog.
func sliceCells(line string, from, to int, end bool) string {
	if to <= from {
		return ""
	}
	var b strings.Builder
	used := 0
	for i := 0; i < len(line); {
		n, esc := escapeLen(line[i:])
		if esc {
			b.WriteString(line[i : i+n])
			i += n
			continue
		}
		size := decodeRune(line[i:])
		if used >= from && used < to {
			b.WriteString(line[i : i+size])
		}
		used += runeCells(line[i : i+size])
		i += size
		if used >= to {
			if end {
				b.WriteString(zoneMarks(line[i:]))
			}
			break
		}
	}
	return b.String()
}
