package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A dialog drawn over blank screen rows must keep its left edge straight.
// The screen is padded with empty lines, which have no cells to the left of
// the dialog, and the rows used to slide to column 0.
func TestOverlayKeepsAlignmentOverBlankRows(t *testing.T) {
	base := "aaaaaaaaaaaaaaaaaaaa\n\n\n"
	rows := strings.Split(overlay(base, "XXXX\nXXXX\nXXXX", 20, 4), "\n")
	for i, row := range rows[:3] {
		if got := strings.Index(row, "XXXX"); got != 8 {
			t.Errorf("row %d: dialog starts at cell %d, want 8 (%q)", i, got, row)
		}
	}
}

// boxRows returns the rows of a rendered screen between two side borders,
// so a test can tell a whole dialog from one cut off at the right edge of
// the screen.
func boxRows(view string) []string {
	var rows []string
	for _, line := range strings.Split(stripANSI(view), "\n") {
		if l, r := strings.Index(line, "│"), strings.LastIndex(line, "│"); l >= 0 && r > l {
			rows = append(rows, line[l:r+len("│")])
		}
	}
	return rows
}

// A confirmation on a narrow terminal is drawn narrower, rather than at
// its usual width with the buttons and the right border cut off.
func TestConfirmationFitsANarrowScreen(t *testing.T) {
	f := sampleAPI()
	h := newHarness(t, f)
	h.press("2", "j", "d") // a shared list's delete, the widest prompt
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, want a confirmation", h.model.layer)
	}
	for _, width := range []int{40, 30} {
		h.send(tea.WindowSizeMsg{Width: width, Height: 30})
		view := stripANSI(h.view())
		buttons := false
		for _, row := range boxRows(view) {
			if strings.Contains(row, "yes") && strings.Contains(row, "no") {
				buttons = true
			}
		}
		if !buttons {
			t.Errorf("width %d: no row holds both buttons inside the box\n%s", width, view)
		}
		if !strings.Contains(view, "cannot be") || !strings.Contains(view, "undone") {
			t.Errorf("width %d: the warning was cut\n%s", width, view)
		}
	}
	h.press("enter", "enter")
	if len(f.lists) != 2 {
		t.Errorf("lists = %d, want the list deleted from the narrow dialog", len(f.lists))
	}
}

// The palette keeps its keys and its right border on a screen narrower
// than its usual minimum width.
func TestPaletteFitsANarrowScreen(t *testing.T) {
	h := newHarness(t, sampleAPI())
	h.send(tea.WindowSizeMsg{Width: 26, Height: 30})
	h.press(":")
	if h.model.layer != layerPalette {
		t.Fatalf("layer = %v, want the palette", h.model.layer)
	}
	view := stripANSI(h.view())
	row := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "new todo") {
			row = strings.TrimRight(line, " ")
		}
	}
	if !strings.HasSuffix(row, " n  │") {
		t.Errorf("the palette's row is cut off before its key and border: %q\n%s", row, view)
	}
	// Widening the screen with the palette open draws it wider.
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := h.model.palette.width; got != 72 {
		t.Errorf("palette width after widening = %d, want 72", got)
	}
}
