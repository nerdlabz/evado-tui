package local

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/nerdlabz/evado-tui/internal/api"
)

func units(s string) int { return len(utf16.Encode([]rune(s))) }

func TestATitleIsCutTheWayTheServiceStoresIt(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"short", "Pay rent", "Pay rent"},
		{"over the limit", strings.Repeat("a", 600), strings.Repeat("a", 500)},
		{"at the limit", strings.Repeat("a", 500), strings.Repeat("a", 500)},
		{"one over", strings.Repeat("a", 501), strings.Repeat("a", 500)},
		{"spaces around", "  \t Pay rent \n ", "Pay rent"},
		{"spaces not counted", "  " + strings.Repeat("a", 500) + "  ", strings.Repeat("a", 500)},
		// The service trims what it is sent, so a space left last by the
		// cut would not come back.
		{"space left at the cut", strings.Repeat("a", 499) + " b", strings.Repeat("a", 499)},
		{"spaces left at the cut", strings.Repeat("a", 400) + strings.Repeat(" ", 150) + "b", strings.Repeat("a", 400)},
		// Emoji are two units each: 250 fit.
		{"emoji", strings.Repeat("😀", 300), strings.Repeat("😀", 250)},
		// 499 units and then a pair that would end at 501: the pair goes.
		{"cut inside a pair", strings.Repeat("a", 499) + "😀", strings.Repeat("a", 499)},
		{"pair ending at the limit", strings.Repeat("a", 498) + "😀", strings.Repeat("a", 498) + "😀"},
		// Runes are not units: these are one unit each.
		{"letters count as one", strings.Repeat("é", 501), strings.Repeat("é", 500)},
		// JavaScript's trim removes a byte order mark and keeps U+0085.
		{"byte order mark", "\uFEFFPay rent ", "Pay rent"},
		{"next line", "\u0085Pay rent", "\u0085Pay rent"},
		{"only spaces", " 　 ", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := StoredTitle(c.in)
			if got != c.want {
				t.Errorf("StoredTitle = %d units %q, want %d units %q", units(got), got, units(c.want), c.want)
			}
			if !utf8.ValidString(got) || units(got) > 500 {
				t.Errorf("result is not valid text of at most 500 units: %d units", units(got))
			}
		})
	}
}

func TestACreatedOrEditedTitleIsCutHere(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()
	long := strings.Repeat("a", 600)

	todo := add(t, c, "  "+long)
	if todo.Title != strings.Repeat("a", 500) {
		t.Errorf("created with %d units, want 500", units(todo.Title))
	}
	made, err := c.CreateTodos(ctx, api.NewTodo{Title: strings.Repeat("😀", 300), ScheduledDay: strp("2030-01-02")})
	if err != nil || len(made) != 1 || made[0].Title != strings.Repeat("😀", 250) {
		t.Errorf("created with a date: %v %v, want 250 emoji", made, err)
	}

	short := add(t, c, "Pay rent")
	for title, want := range map[string]string{
		long:                                  strings.Repeat("a", 500),
		"  " + strings.Repeat("a", 499) + "😀": strings.Repeat("a", 499),
		"  Pay the rent \n":                   "Pay the rent",
	} {
		got, _, err := c.UpdateTodo(ctx, short.ID, api.TodoPatch{Title: api.Val(title)})
		if err != nil || got.Title != want {
			t.Errorf("edit to %d units kept %d units %q (%v), want %d units", units(title), units(got.Title), got.Title, err, units(want))
		}
	}
	if _, _, err := c.UpdateTodo(ctx, short.ID, api.TodoPatch{Title: api.Val("   ")}); err == nil {
		t.Error("an edit to nothing was taken")
	}
}

func TestOverlongStepTitlesAreRefusedHere(t *testing.T) {
	ctx := context.Background()
	b := open(t, "", "")
	c := b.Client()
	todo := add(t, c, "Pay rent")
	item := addSteps(t, c, todo.ID, "Call the bank")[0]
	for _, title := range []string{strings.Repeat("a", 501), strings.Repeat("😀", 251), " " + strings.Repeat("a", 500)} {
		if _, err := c.AddChecklistItem(ctx, todo.ID, title); err == nil {
			t.Fatal("overlong step was accepted")
		}
		if _, err := c.UpdateChecklistItem(ctx, item.ID, strp(title), nil); err == nil {
			t.Fatal("overlong edit was accepted")
		}
	}
	if got := checklist(t, c, todo.ID); len(got) != 1 || got[0].Title != "Call the bank" {
		t.Fatalf("failed edits changed the checklist: %+v", got)
	}
}

func TestOldFilesNormalizeTitlesAndKeepOtherPendingEdits(t *testing.T) {
	b := open(t, "", "")
	todo := add(t, b.Client(), "Old title")
	b.mu.Lock()
	item := &b.st.Todos[0]
	item.New = false
	item.Title = strings.Repeat("😀", 300)
	item.Steps = []api.ChecklistItem{{ID: "step", TodoID: todo.ID, Title: strings.Repeat("a", 600)}}
	item.Base = item.based()
	item.Notes = strp("A pending note")
	item.Dirty = []string{fNotes}
	raw, err := json.Marshal(b.st)
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(b.path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.st.Todos[0]
	if units(got.Title) != 500 || units(got.Steps[0].Title) != 500 || got.Notes == nil || *got.Notes != "A pending note" {
		t.Fatalf("normalizing lost an edit: %+v", got)
	}
	for _, field := range []string{fTitle, fChecklist, fNotes} {
		if !contains(got.Dirty, field) {
			t.Errorf("missing pending field %s", field)
		}
	}
	if got.Base == nil || units(got.Base.Title) != 600 || units(got.Base.Steps[0].Title) != 600 {
		t.Fatal("normalizing lost the original service version")
	}
}

func TestLocalTextLimitsUseServiceUnitsAndRejectAtomically(t *testing.T) {
	ctx := context.Background()
	c := open(t, "", "").Client()
	todo := add(t, c, "Keep this")
	for _, notes := range []string{strings.Repeat("a", 10001), strings.Repeat("😀", 5001)} {
		if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "New", Notes: &notes}); err == nil {
			t.Fatal("overlong notes created a todo")
		}
		if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{Title: api.Val("Changed"), Notes: api.Val(notes)}); err == nil {
			t.Fatal("overlong notes accepted")
		}
	}
	if got := get(t, c, todo.ID); got.Title != "Keep this" || got.Notes != nil {
		t.Fatalf("failed patch changed todo: %+v", got)
	}
	if _, err := c.CreateList(ctx, strings.Repeat("😀", 61)); err == nil {
		t.Fatal("overlong list name accepted")
	}
	list, err := c.CreateList(ctx, strings.Repeat("😀", 60))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameList(ctx, list.ID, strings.Repeat("a", 121)); err == nil {
		t.Fatal("overlong list rename accepted")
	}
	if ValidRule(strings.Repeat("0", 64) + "1:day") {
		t.Fatal("overlong repeat rule accepted")
	}
	notes := strings.Repeat("😀", 5000)
	if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{Notes: api.Val(notes)}); err != nil {
		t.Fatal(err)
	}
}

func TestALongTitleEditedHereSyncsOnceAndLeavesNothing(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		skew time.Duration
		word string
	}{
		{"clock behind the service's", 0, "rents "},
		{"clock ahead of the service's", time.Hour, "rents "},
		// The 500th unit is a space, which the service trims off what it
		// is sent.
		{"a space at the cut", 0, "rent "},
		{"a space at the cut, clock ahead", time.Hour, "rent "},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc, b, cl, todo := offlineWith(t, "Pay rent")
			if c.skew != 0 {
				b.now = func() time.Time { return svc.clock.Add(c.skew) }
			}
			long := strings.Repeat(c.word, 600/len(c.word))
			if _, _, err := cl.UpdateTodo(ctx, todo.ID, api.TodoPatch{Title: api.Val(long)}); err != nil {
				t.Fatal(err)
			}
			if _, err := cl.AddChecklistItem(ctx, todo.ID, StoredTitle("step "+long)); err != nil {
				t.Fatal(err)
			}

			svc.down.Store(false)
			svc.got = nil
			res := mustSync(t, b)
			up := svc.todos[todo.ID]
			if units(up.Title) < 499 || units(up.Title) > 500 {
				t.Errorf("service holds %d units, want a title cut to 500", units(up.Title))
			}
			if got := get(t, cl, todo.ID); got.Title != up.Title {
				t.Errorf("kept here: %d units, service has %d; want the same", units(got.Title), units(up.Title))
			}
			steps := checklist(t, cl, todo.ID)
			if len(steps) != 1 || len(up.Checklist) != 1 || steps[0].Title != up.Checklist[0].Title || units(steps[0].Title) < 499 {
				t.Errorf("steps here = %+v, service has %+v; want the same, cut", steps, up.Checklist)
			}
			if n := svc.pushes(todo.ID); n != 1 {
				t.Errorf("pushed %d times, want 1", n)
			}
			if len(res.Replaced) != 0 || len(res.Refused) != 0 || b.Status().Pending != 0 {
				t.Errorf("replaced %v, refused %v, pending %d; want nothing reported or left", res.Replaced, res.Refused, b.Status().Pending)
			}
			if again := mustSync(t, b); again.Pushed != 0 {
				t.Errorf("a second sync pushed %d, want 0", again.Pushed)
			}
		})
	}
}

// JavaScript's trim removes a byte order mark and Go's does not, so a title
// of nothing else must not slip through as a todo with no title: the service
// would call it Untitled and the two would never agree.
func TestATitleOfOnlyAByteOrderMarkIsNotATodo(t *testing.T) {
	ctx := context.Background()
	c := open(t, "", "").Client()
	if made, err := c.CreateTodos(ctx, api.NewTodo{Title: "\uFEFF"}); err == nil {
		t.Errorf("a title of only a byte order mark made %v", made)
	}
	made, err := c.CreateTodos(ctx, api.NewTodo{Title: "\uFEFF\nbuy milk\n\uFEFF "})
	if err != nil || len(made) != 1 || made[0].Title != "buy milk" {
		t.Errorf("made %v (%v), want only buy milk", made, err)
	}
}
