package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// confirm is a yes/no modal for destructive actions.
type confirm struct {
	title string
	body  string
	// danger styles the prompt in red and requires a second press.
	danger bool
	yes    bool
	// armed is set once a danger prompt has been confirmed, so a stray
	// enter cannot delete anything.
	armed bool
	id    string
	// width is the screen's, which the dialog has to fit inside.
	width int
}

func newConfirm(id, title, body string, danger bool) *confirm {
	return &confirm{id: id, title: title, body: body, danger: danger, yes: true}
}

func (c *confirm) update(msg tea.Msg) (*confirm, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return c, nil
	}
	switch key.String() {
	case "y", "Y":
		return c, nil
	case "n", "N", "esc", "q":
		c.yes = false
		return c, nil
	case "left", "h", "right", "l", "tab", "shift+tab", " ":
		c.yes = !c.yes
		return c, nil
	case "enter":
		if c.danger && !c.armed {
			// First enter arms, second commits.
			c.armed = true
			return c, nil
		}
		return c, nil
	}
	return c, nil
}

func (c *confirm) view() string {
	tone := colAccent
	if c.danger {
		tone = colRed
	}
	on := lipgloss.NewStyle().Foreground(lipgloss.Color(pillText(string(tone)))).Background(tone).Bold(true)
	off := lipgloss.NewStyle().Foreground(colMuted).Background(colPanel)
	yes, no := off.Render("  yes  "), on.Render("  no  ")
	if c.yes {
		yes, no = on.Render("  yes  "), off.Render("  no  ")
	}
	// Wrap first, then size the box to the wrapped result, so a long
	// sentence cannot push the dialog off the screen.
	inner := max(visibleWidth(c.body)+2, 44)
	inner = min(inner, 72)
	if c.width > 0 {
		// The border and padding take six cells. A box wider than the
		// screen is cut off on the right, which is where the buttons are.
		inner = max(min(inner, c.width-6), 15)
	}
	wrap := func(s string) string { return strings.Join(wrapText(s, inner), "\n") }
	body := lipgloss.NewStyle().Foreground(colMuted).Render(wrap(c.body))
	if c.danger && c.armed {
		body += "\n\n" + styErr.Bold(true).Render(wrap("press enter again to confirm"))
	} else if c.danger {
		body += "\n\n" + styFaint.Render(wrap("this cannot be undone"))
	}
	title := lipgloss.NewStyle().Foreground(tone).Bold(true).Render(wrap(c.title))
	buttons := yes + "  " + no
	if hint := "   " + styFaint.Render("y · n · esc"); visibleWidth(buttons+hint) <= inner {
		buttons += hint
	}
	content := title + "\n\n" + body + "\n\n" + buttons
	return styBox.BorderForeground(tone).Padding(1, 2).Render(padLines(content, inner))
}

// wrapBlock joins wrapped text back into a single indented block.
func wrapBlock(s string, w int) string {
	lines := wrapText(s, w)
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// toastKind styles a status message.
type toastKind int

const (
	toastInfo toastKind = iota
	toastOK
	toastWarn
	toastError
)

// toast is a transient message floated over the bottom of the screen.
type toast struct {
	kind toastKind
	text string
	// lasting marks a sync warning, which may be the only notice that an
	// edit was lost, so it waits out the sign-in screen rather than
	// expiring behind it.
	lasting bool
}

func (t *toast) color() lipgloss.Color {
	switch t.kind {
	case toastOK:
		return colGreen
	case toastWarn:
		return colAmber
	case toastError:
		return colRed
	default:
		return colAccent
	}
}

func (t *toast) style() lipgloss.Style { return lipgloss.NewStyle().Foreground(t.color()) }

func (t *toast) prefix() string {
	switch t.kind {
	case toastOK:
		return "✓ "
	case toastWarn:
		return "! "
	case toastError:
		return "✗ "
	default:
		return "· "
	}
}

// helpEntry is one row of the help overlay.
type helpEntry struct {
	keys string
	desc string
}

// help is the full-screen, scrollable key reference.
type help struct {
	sections []helpSection
	width    int
	height   int
	offset   int
}

type helpSection struct {
	name    string
	entries []helpEntry
}

func newHelp(sections []helpSection) *help {
	return &help{sections: sections}
}

func (h *help) update(msg tea.Msg) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return
	}
	switch key.String() {
	case "up", "k":
		h.offset--
	case "down", "j":
		h.offset++
	case "pgup", "ctrl+b":
		h.offset -= h.height
	case "pgdown", "ctrl+f", " ":
		h.offset += h.height
	case "home", "g":
		h.offset = 0
	case "end", "G":
		h.offset = h.lineCount()
	}
	h.clamp()
}

func (h *help) lineCount() int { return len(h.lines()) }

func (h *help) clamp() {
	maxOff := max(h.lineCount()-h.height, 0)
	if h.offset > maxOff {
		h.offset = maxOff
	}
	if h.offset < 0 {
		h.offset = 0
	}
}

// helpColumn is the narrowest a column of keys is drawn.
const helpColumn = 58

// lines lays the sections out in as many columns as fit, filling each
// column in turn so the reading order runs down, then across.
func (h *help) lines() []string {
	n := min(max(h.width/helpColumn, 1), 3)
	colW := h.width / n
	if n == 1 {
		// One column does not need the whole screen to itself.
		colW = min(colW, 78)
	}
	total := 0
	for _, s := range h.sections {
		total += 2 + len(s.entries)
	}
	cols := make([][]string, n)
	at := 0
	for _, s := range h.sections {
		// Move on once this column has its share, unless it is the last.
		if at < n-1 && len(cols[at]) > 0 && len(cols[at])+len(s.entries)/2 >= (total+n-1)/n {
			at++
		}
		cols[at] = append(cols[at], brandGradient(s.name))
		for _, e := range s.entries {
			row := " " + pad(styKey.Render(e.keys), 15) + styMuted.Render(e.desc)
			if e.keys == "" {
				row = " " + styFaint.Render(e.desc)
			}
			cols[at] = append(cols[at], truncate(row, colW-2))
		}
		cols[at] = append(cols[at], "")
	}
	rows := 0
	for _, c := range cols {
		rows = max(rows, len(c))
	}
	blocks := make([]string, n)
	for i, c := range cols {
		blocks[i] = clipBlock(strings.Join(c, "\n"), colW, rows)
	}
	return strings.Split(joinPanes(blocks...), "\n")
}

func (h *help) view() string {
	h.clamp()
	rows := h.lines()
	more := ""
	if len(rows) > h.height {
		more = fmt.Sprintf("  ·  j/k scroll  ·  %d%%", min((h.offset+h.height)*100/len(rows), 100))
	}
	rows = rows[min(h.offset, len(rows)):]
	if len(rows) > h.height {
		rows = rows[:h.height]
	}
	title := styBold.Render("Keys") + "  " + styFaint.Render("esc or ? to close"+more)
	return lipgloss.JoinVertical(lipgloss.Left, title, "", strings.Join(rows, "\n"))
}
