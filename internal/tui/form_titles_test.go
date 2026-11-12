package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
)

func (h *harness) paste(s string) {
	h.t.Helper()
	h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true})
	h.run(h.cmds...)
	h.cmds = nil
}

// titled returns the todos here whose title starts with prefix.
func (h *harness) titled(prefix string) []api.Todo {
	h.t.Helper()
	all, err := h.model.client.ListTodos(context.Background(), api.TodoFilter{Status: api.StatusAll})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []api.Todo
	for _, t := range all {
		if strings.HasPrefix(t.Title, prefix) {
			out = append(out, t)
		}
	}
	return out
}

// A title pasted longer than the service keeps is cut in the form, the way
// the service stores it, so the todo saved is the one the form showed.
func TestAPastedTitleIsCutInTheFormAsItWillBeKept(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	// Each house is two UTF-16 code units, so 300 of them do not fit.
	long := "Pay rent " + strings.Repeat("🏠", 300)
	h.press("n")
	h.paste(long)
	shown := h.model.form.valueOf("title")
	if want := local.StoredTitle(long); shown != want {
		t.Fatalf("the form shows %d units, want the %d the service keeps", local.TextUnits(shown), local.TextUnits(want))
	}
	h.press("enter")
	if got := h.titled("Pay rent"); len(got) != 1 || got[0].Title != shown {
		t.Fatalf("saved %+v, want the title the form showed", got)
	}
}

// In quick capture each line is a todo, so each line is cut on its own.
func TestQuickCaptureCutsEachLongLine(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	long := "Long " + strings.Repeat("a", 600)
	h.press("N")
	h.paste("Short one\n" + long)
	want := "Short one\n" + local.StoredTitle(long)
	if shown := h.model.form.valueOf("title"); shown != want {
		t.Fatalf("the form shows %q, want each line as it will be kept", truncate(shown, 40))
	}
}

// The store refuses a step longer than a title can be, so a long paste
// into the step form is cut there instead of failing on save.
func TestALongStepIsCutInTheForm(t *testing.T) {
	h := offlineHarness(t, config.Config{Local: true})
	h.press("n")
	h.typeText("Water the plants")
	h.press("enter")
	h.press("enter", "tab", "c")
	if h.model.form == nil || h.model.form.id != formNewStep {
		t.Fatal("the step form did not open")
	}
	long := strings.Repeat("b", 600)
	h.paste(long)
	h.press("enter")
	todo := h.titled("Water the plants")
	if len(todo) != 1 {
		t.Fatalf("todos = %+v", todo)
	}
	steps, err := h.model.client.ListChecklist(context.Background(), todo[0].ID)
	if err != nil || len(steps) != 1 || steps[0].Title != local.StoredTitle(long) {
		t.Fatalf("steps = %+v, err %v; want the step cut to %d units", steps, err, local.MaxTitle)
	}
}
