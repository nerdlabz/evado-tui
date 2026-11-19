package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/nerdlabz/evado-tui/internal/config"
)

func newMouseHarness(t *testing.T, tweak func(*config.Config)) *harness {
	t.Helper()
	return newHarnessWith(t, sampleAPI(), func(c *config.Config) {
		c.Mouse = true
		if tweak != nil {
			tweak(c)
		}
	})
}

func (h *harness) find(text string) (x, y int) {
	h.t.Helper()
	for y, line := range strings.Split(ansi.Strip(h.view()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	h.t.Fatalf("%q is not on screen:\n%s", text, ansi.Strip(h.view()))
	return 0, 0
}

func (h *harness) findOn(label, text string) (x, y int) {
	h.t.Helper()
	for y, line := range strings.Split(ansi.Strip(h.view()), "\n") {
		if strings.Contains(line, label) {
			if i := strings.Index(line, text); i >= 0 {
				return ansi.StringWidth(line[:i]), y
			}
		}
	}
	h.t.Fatalf("%q is not on screen beside %q:\n%s", text, label, ansi.Strip(h.view()))
	return 0, 0
}

func (h *harness) mouse(x, y int, b tea.MouseButton) {
	h.t.Helper()
	h.view()
	h.send(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress})
	h.run(h.cmds...)
	h.cmds = nil
}

func (h *harness) click(text string) {
	h.t.Helper()
	x, y := h.find(text)
	h.mouse(x, y, tea.MouseButtonLeft)
}

func (h *harness) doubleClick(text string) {
	h.t.Helper()
	x, y := h.find(text)
	h.mouse(x, y, tea.MouseButtonLeft)
	h.mouse(x, y, tea.MouseButtonLeft)
}

func TestClickingTheSidebarShowsTheViewClicked(t *testing.T) {
	for _, demo := range []bool{false, true} {
		h := newMouseHarness(t, func(c *config.Config) { c.Demo = demo })
		h.click("▲ Overdue")
		if h.model.todos.filter.status != "overdue" {
			t.Errorf("demo=%v: clicking Overdue shows %q", demo, h.model.todos.filter.status)
		}
		h.click("◇ Upcoming")
		if h.model.todos.filter.status != "upcoming" {
			t.Errorf("demo=%v: clicking Upcoming shows %q", demo, h.model.todos.filter.status)
		}
		h.click("◆ Everything")
		if h.model.todos.filter.mode != viewEverything {
			t.Errorf("demo=%v: clicking Everything shows %v", demo, h.model.todos.filter.mode)
		}
	}
}

func TestClickingARowSelectsItAndADoubleClickOpensIt(t *testing.T) {
	for _, demo := range []bool{false, true} {
		h := newMouseHarness(t, func(c *config.Config) { c.Demo = demo })
		h.click("Renew passport")
		if got := h.model.todos.selected().Title; got != "Renew passport" {
			t.Fatalf("demo=%v: selected %q, want Renew passport", demo, got)
		}
		h.doubleClick("Buy milk")
		if !h.model.inDetail() || h.model.todos.detail.Title != "Buy milk" {
			t.Errorf("demo=%v: a double click should open Buy milk, open is %q", demo, h.model.todos.detail.Title)
		}
	}
}

func TestClickingTheCircleCompletesThatTodoOnly(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.press("g")
	x, y := h.find("Renew passport")
	h.mouse(x-2, y, tea.MouseButtonLeft)
	for _, todo := range h.model.todos.items {
		switch todo.Title {
		case "Renew passport":
			if !todo.Completed() {
				t.Error("clicking its circle should complete Renew passport")
			}
		case "Pay the cable bill":
			if todo.Completed() {
				t.Error("only the todo clicked should be completed")
			}
		}
	}
}

func TestWheelMovesTheListUnderThePointer(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.press("g")
	first := h.model.todos.selected().ID
	x, y := h.find("Renew passport")
	h.mouse(x, y, tea.MouseButtonWheelDown)
	if h.model.todos.selected().ID == first {
		t.Error("the wheel over the list should move its cursor")
	}
	sx, sy := h.find("◆ Everything")
	before := h.model.todos.selected().ID
	h.mouse(sx, sy, tea.MouseButtonWheelUp)
	if h.model.todos.selected().ID != before {
		t.Error("the wheel over the sidebar should leave the list alone")
	}
}

func TestClickingTabsAndRowsOnOtherTabs(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.click("Lists")
	if h.model.tab != tabLists {
		t.Fatalf("tab = %v, want lists", h.model.tab)
	}
	x, y := h.find("● Household")
	h.mouse(x, y, tea.MouseButtonLeft)
	if l := h.model.listsV.selected(); l == nil || l.Name != "Household" {
		t.Errorf("clicking Household selected %v", l)
	}
	h.mouse(x, y, tea.MouseButtonLeft)
	if h.model.tab != tabTodos || h.model.todos.filter.listID != "l2" {
		t.Errorf("a double click on a list should show its todos, tab %v list %q", h.model.tab, h.model.todos.filter.listID)
	}

	h.click("Account")
	if h.model.tab != tabAccount {
		t.Fatalf("tab = %v, want account", h.model.tab)
	}
	menu := h.model.acct.menu()
	last := menu[len(menu)-1]
	h.click(last.label)
	if h.model.acct.selected().key != last.key {
		t.Errorf("clicking %q selected %q", last.label, h.model.acct.selected().label)
	}
}

func TestClickingAFooterHintPressesItsKey(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.click("? help")
	if h.model.layer != layerHelp {
		t.Fatalf("layer = %v, want help", h.model.layer)
	}
	h.mouse(0, h.model.height-1, tea.MouseButtonLeft)
	if h.model.layer != layerNone {
		t.Errorf("a click beside the help should close it, layer = %v", h.model.layer)
	}
}

func TestClickingInThePalette(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.click(": commands")
	if h.model.layer != layerPalette {
		t.Fatalf("layer = %v, want palette", h.model.layer)
	}
	h.click("Command palette")
	if h.model.layer != layerPalette {
		t.Error("a click inside the palette should leave it open")
	}
	h.mouse(0, h.model.height-1, tea.MouseButtonLeft)
	if h.model.layer != layerNone {
		t.Errorf("a click beside the palette should close it, layer = %v", h.model.layer)
	}
}

func TestClickingConfirmButtons(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.click("Renew passport")
	h.press("d")
	if h.model.layer != layerConfirm {
		t.Fatalf("layer = %v, want confirm", h.model.layer)
	}
	h.click(" no ")
	if h.model.layer != layerNone {
		t.Fatalf("no should close the question, layer = %v", h.model.layer)
	}
	h.mustContain("Renew passport")

	h.press("d")
	h.click(" yes ")
	if h.model.layer != layerNone {
		t.Fatalf("yes should answer the question, layer = %v", h.model.layer)
	}
	for _, todo := range h.model.todos.items {
		if todo.Title == "Renew passport" {
			t.Error("yes should delete Renew passport")
		}
	}
}

func TestClickingInAForm(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.press("n")
	if h.model.layer != layerForm {
		t.Fatalf("layer = %v, want form", h.model.layer)
	}
	f := h.model.form
	choice := -1
	for i, fl := range f.fields {
		if fl.kind == fieldSelect && len(fl.options) > 1 && fl.value != fl.options[1] {
			choice = i
			break
		}
	}
	if choice >= 0 {
		want := f.fields[choice].options[1]
		x, y := h.findOn(f.fields[choice].label, " "+want+" ")
		h.mouse(x, y, tea.MouseButtonLeft)
		if f.focus != choice || f.fields[choice].value != want {
			t.Errorf("clicking %q: focus %d value %q", want, f.focus, f.fields[choice].value)
		}
	}
	h.click("  cancel  ")
	if h.model.layer != layerNone {
		t.Errorf("cancel should close the form, layer = %v", h.model.layer)
	}
}

func TestClickingTheChecklist(t *testing.T) {
	h := newMouseHarness(t, nil)
	h.send(tea.WindowSizeMsg{Width: 150, Height: 34})
	h.doubleClick("Pay the cable bill")
	if !h.model.inDetail() {
		t.Fatal("a double click should open the todo")
	}
	h.click("read the meter")
	if h.model.focus != focusChecklist || h.model.todos.checklist.selected() != 0 {
		t.Fatalf("clicking a step should select it, focus %v", h.model.focus)
	}
	x, y := h.find("read the meter")
	h.mouse(x-2, y, tea.MouseButtonLeft)
	if !h.model.todos.steps[0].Completed() {
		t.Error("clicking a step's box should tick it")
	}
}

func TestZonesSurviveCutsAndSplices(t *testing.T) {
	zs := &zones{}
	drawing = zs
	row := clickable("hello world", func(bool) tea.Cmd { return nil })
	cut := truncate(row, 6)
	toast := spliceLine(row, "XX", 3, 2)
	drawing = nil

	if got := zs.place(cut); got != "hello…" {
		t.Errorf("placed %q, want the markers gone", got)
	}
	if z := zs.list[0]; !z.placed || z.x0 != 0 || z.x1 != 6 {
		t.Errorf("a cut row spans %d..%d, placed %v", z.x0, z.x1, z.placed)
	}
	zs.list[0].placed = false
	if got := zs.place(toast); got != "helXX world" {
		t.Errorf("placed %q", got)
	}
	if z := zs.list[0]; !z.placed || z.x0 != 0 || z.x1 != 11 {
		t.Errorf("a spliced row spans %d..%d, placed %v", z.x0, z.x1, z.placed)
	}
}

func TestClickingTheWaysToSignIn(t *testing.T) {
	m := New(t.Context(), config.Config{Mouse: true})
	m.ready = true
	h := &harness{t: t, model: m}
	h.send(tea.WindowSizeMsg{Width: 90, Height: 30})
	h.click("Use an email code or an API token")
	if !strings.Contains(h.view(), "evt_") {
		t.Errorf("a click should open the token step:\n%s", h.view())
	}
}

func TestTurningTheMouseOnAndOffInTheApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("EVADO_CONFIG", path)
	h := newHarness(t, sampleAPI())

	h.press(":")
	for _, r := range "mouse" {
		h.press(string(r))
	}
	h.press("enter")
	if !h.model.cfg.Mouse {
		t.Fatal("the palette entry should turn the mouse on")
	}
	if saved, err := config.Load(); err != nil || !saved.Mouse {
		t.Errorf("turning the mouse on should be saved, got %v, %v", saved.Mouse, err)
	}

	h.click("Account")
	h.click("Mouse")
	h.mustContain("Mouse: on")
	h.press("enter")
	if h.model.cfg.Mouse {
		t.Fatal("enter on the Mouse section should turn it off")
	}
	if saved, err := config.Load(); err != nil || saved.Mouse {
		t.Errorf("turning the mouse off should be saved, got %v, %v", saved.Mouse, err)
	}
	h.mustContain("Mouse: off")
}
