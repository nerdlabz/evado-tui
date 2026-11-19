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

const wheelStep = 3

func (m *Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.cfg.Mouse || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	hits := m.zones.at(msg.X, msg.Y)
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		for _, z := range hits {
			if z.wheel != nil {
				return m, z.wheel(delta)
			}
		}
	case tea.MouseButtonLeft:
		again := m.lastClick.x == msg.X && m.lastClick.y == msg.Y && time.Since(m.lastClick.at) < doubleClick
		m.lastClick = clickAt{x: msg.X, y: msg.Y, at: time.Now()}
		if again {
			// A third click starts a new pair rather than opening twice.
			m.lastClick = clickAt{}
		}
		for _, z := range hits {
			if z.click != nil {
				return m, z.click(again)
			}
		}
	}
	return m, nil
}

// press does what a key does where the keyboard is now. The mouse means
// the key it stands for, so the user's remapping does not apply.
func (m *Model) press(k tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	if m.layer == layerNone {
		_, cmd = m.dispatchKey(k)
	} else {
		_, cmd = m.onKey(k)
	}
	return cmd
}

func (m *Model) pressKey(name string) tea.Cmd {
	k, ok := parseKey(name)
	if !ok {
		return nil
	}
	return m.press(k)
}

func (m *Model) wheelKeys(delta int) tea.Cmd {
	k := tea.KeyMsg{Type: tea.KeyDown}
	if delta < 0 {
		k = tea.KeyMsg{Type: tea.KeyUp}
	}
	var cmds []tea.Cmd
	for i := 0; i < wheelStep; i++ {
		cmds = append(cmds, m.press(k))
	}
	return tea.Batch(cmds...)
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
