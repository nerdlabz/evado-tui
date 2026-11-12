package tui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// -- key remapping ----------------------------------------------------------

var namedKeys = map[string]tea.KeyType{
	"enter":     tea.KeyEnter,
	"esc":       tea.KeyEsc,
	"tab":       tea.KeyTab,
	"shift+tab": tea.KeyShiftTab,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
	"backspace": tea.KeyBackspace,
}

// parseKey turns a key name from the config into the message the program
// would receive for it.
func parseKey(name string) (tea.KeyMsg, bool) {
	if name == "space" || name == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, true
	}
	if t, ok := namedKeys[name]; ok {
		return tea.KeyMsg{Type: t}, true
	}
	if len(name) == 6 && strings.HasPrefix(name, "ctrl+") && name[5] >= 'a' && name[5] <= 'z' {
		return tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(name[5]-'a')}, true
	}
	if r := []rune(name); len(r) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r}, true
	}
	return tea.KeyMsg{}, false
}

// remapKey applies the user's key remapping. A key with no mapping, or one
// mapped to a name that is not a key, passes through unchanged.
func remapKey(keys map[string]string, msg tea.KeyMsg) tea.KeyMsg {
	to, ok := keys[msg.String()]
	if !ok {
		return msg
	}
	if km, ok := parseKey(to); ok {
		return km
	}
	return msg
}

// -- mouse ------------------------------------------------------------------

// clickAt is a mouse click, kept to recognise a double click.
type clickAt struct {
	x, y int
	at   time.Time
}

const doubleClick = 400 * time.Millisecond

// tabsRow is the screen row the tab strip is drawn on.
const tabsRow = 0

// onMouse handles the wheel and clicks. Mouse reporting is opt-in, so this
// only runs when the config asks for it.
func (m *Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.cfg.Mouse || m.layer != layerNone || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if m.focus != focusList {
			return m, nil
		}
		key := tea.KeyMsg{Type: tea.KeyDown}
		if msg.Button == tea.MouseButtonWheelUp {
			key = tea.KeyMsg{Type: tea.KeyUp}
		}
		var cmds []tea.Cmd
		for i := 0; i < 3; i++ {
			_, c := m.onViewKey(key)
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)
	case tea.MouseButtonLeft:
		return m, m.onClick(msg.X, msg.Y)
	}
	return m, nil
}

func (m *Model) onClick(x, y int) tea.Cmd {
	again := m.lastClick.x == x && m.lastClick.y == y && time.Since(m.lastClick.at) < doubleClick
	m.lastClick = clickAt{x: x, y: y, at: time.Now()}

	if y == tabsRow {
		for i, name := range tabNames {
			if at := tabX(tab(i)); x >= at && x < at+len(name)+2 {
				return m.switchTab(tab(i))
			}
		}
		return nil
	}
	if m.tab != tabTodos {
		return nil
	}
	return m.todos.click(m, x, y-headerRows, again)
}

// click handles a press inside the Todos tab, at a position measured from
// the top left of the view: a sidebar item is shown, and a todo is
// selected, or opened on the second click.
func (v *todosView) click(m *Model, x, y int, again bool) tea.Cmd {
	l := v.layout(m.width, m.bodyHeight())
	if x < l.sideW {
		_, itemAt := v.sidebarLines(l.sideW-2, l.h-2)
		if r := y - 1; r >= 0 && r < len(itemAt) && itemAt[r] >= 0 {
			v.side = itemAt[r]
			m.focus = focusList
			return v.applySide(m, v.sideItems()[v.side])
		}
		return nil
	}
	if x >= l.sideW+l.mainW || (m.inDetail() && l.prevW == 0) {
		return nil
	}
	m.focus = focusList
	if v.repair != nil {
		// Its todos are behind the repair notice, not on it.
		return nil
	}
	edge := 0
	if framed(l.mainW, l.h) {
		edge = 1
	}
	x, y = x-l.sideW-edge, y-edge
	if x < 0 || y < 0 {
		return nil
	}
	if v.filter.board {
		if !v.boardClick(x, y, l.mainW-2*edge, l.h-2*edge) {
			return nil
		}
	} else {
		idx := v.rows.offset + y
		if idx >= len(v.lines) || v.lines[idx].header {
			return nil
		}
		v.rows.cursor = idx
		v.rows.clamp()
	}
	if again {
		_, cmd := m.onViewKey(tea.KeyMsg{Type: tea.KeyEnter})
		return cmd
	}
	return nil
}

// boardHead is how many rows a board column spends on its heading.
const boardHead = 3

// boardClick puts the board's cursor on the card at x, y, reporting
// whether there is one.
func (v *todosView) boardClick(x, y, w, h int) bool {
	_, cw := v.boardWindow(w)
	col := v.boff + x/max(cw, 1)
	if col >= len(v.bcols) || y < boardHead {
		return false
	}
	row := (y - boardHead) / cardRows
	if col == v.bcol {
		// The cursor's own column may be scrolled.
		if fit := max((h-boardHead)/cardRows, 1); v.brow >= fit {
			row += v.brow - fit + 1
		}
	}
	if row >= len(v.bcols[col].idxs) {
		return false
	}
	v.bcol, v.brow = col, row
	return true
}

// -- polling and background work -------------------------------------------

// onPoll refreshes what is on screen. It only runs when the user is not in
// the middle of something: a refresh under an open form or a running
// request would be a surprise, so that tick is skipped and the next one
// tries again.
func (m *Model) onPoll() tea.Cmd {
	next := pollCmd(m.cfg.PollInterval())
	if m.backend != nil {
		// The todos on screen come from this computer, so the poll is what
		// fetches other devices' changes. It runs in the background and
		// only touches the screen if something actually changed.
		switch st := m.backend.Status(); {
		case m.layer == layerLogin || m.syncQueued:
		case st.NeedsSignIn:
			return tea.Batch(next, m.signIn(false))
		case st.SignedIn && !time.Now().Before(m.syncRetry):
			// After a failure the poll waits out the backoff, as the sync
			// after a change does, so a server that is down is not asked
			// again on every poll.
			return tea.Batch(next, m.syncNow())
		}
		return next
	}
	if !m.hasAccount() || !m.ready || m.busy || m.layer != layerNone {
		return next
	}
	return tea.Batch(next, m.refreshAll(false))
}

// -- external editor --------------------------------------------------------

// editorCommand is the user's editor: $VISUAL, then $EDITOR, then
// whatever the platform has.
func editorCommand() (string, []string) {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(name)); len(f) > 0 {
			return f[0], f[1:]
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad", nil
	}
	return "vi", nil
}

// openEditor hands the focused notes field to $EDITOR. The text comes back
// in an editorMsg once the editor exits.
func (m *Model) openEditor() tea.Cmd {
	f := m.form
	if f == nil || !f.focusedMultiline() {
		return m.warnf("ctrl+e opens the editor on a notes field")
	}
	f.syncValues()
	fl := f.fields[f.focus]
	tmp, err := os.CreateTemp("", "evado-*.md")
	if err != nil {
		return m.warnf("could not open the editor: %s", errText(err))
	}
	path := tmp.Name()
	_, werr := tmp.WriteString(fl.value)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return m.warnf("could not open the editor: %s", errText(werr))
	}
	bin, args := editorCommand()
	key := fl.key
	return tea.ExecProcess(exec.Command(bin, append(args, path)...), func(err error) tea.Msg {
		b, rerr := os.ReadFile(path)
		_ = os.Remove(path)
		if err == nil {
			err = rerr
		}
		return editorMsg{key: key, text: string(b), err: err}
	})
}

// onEditor puts the edited text back into the form.
func (m *Model) onEditor(msg editorMsg) tea.Cmd {
	if msg.err != nil {
		return m.warnf("the editor failed: %s", errText(msg.err))
	}
	if m.form == nil {
		return nil
	}
	for _, fl := range m.form.fields {
		if fl.key == msg.key && fl.kind == fieldArea {
			fl.area.SetValue(strings.TrimRight(msg.text, "\n"))
			fl.value = fl.area.Value()
		}
	}
	return nil
}
