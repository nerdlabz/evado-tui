package local

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// A list that leaves the account (deleted by its owner, or this account
// removed from it) takes its todos with it, but a todo with changes the
// service never heard is not dropped without a word: one made here moves to
// the personal list, and the rest is reported by title.

// tripWith is a backend synced with the stand-in service, which has a list
// "trip" that the test deletes as someone else would, while the network is
// gone. setup runs offline, in between.
func tripWith(t *testing.T, setup func(svc *service, b *Backend, c *api.Client)) (*service, *Backend, Result) {
	t.Helper()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "trip", Name: "Trip", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	setup(svc, b, b.Client())
	svc.dropList("trip")
	svc.down.Store(false)
	return svc, b, mustSync(t, b)
}

// held creates a todo in a list and syncs, so both sides know it.
func held(t *testing.T, svc *service, b *Backend, title, list string) api.Todo {
	t.Helper()
	made, err := b.Client().CreateTodos(context.Background(), api.NewTodo{Title: title, ListID: list})
	if err != nil {
		t.Fatal(err)
	}
	mustSync(t, b)
	return made[0]
}

func TestATodoMadeHereInAListThatLeftMovesToThePersonalList(t *testing.T) {
	ctx := context.Background()
	var made []api.Todo
	svc, b, res := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		svc.down.Store(true)
		var err error
		// The assignee is someone who is not in the personal list.
		made, err = c.CreateTodos(ctx, api.NewTodo{Title: "Book the ferry", ListID: "trip", AssigneeID: strp("u2")})
		if err != nil {
			t.Fatal(err)
		}
	})
	want := []GoneList{{Name: "Trip", Into: "Personal", Moved: []string{"Book the ferry"}}}
	if len(res.ListsGone) != 1 || res.ListsGone[0].Name != want[0].Name || res.ListsGone[0].Into != "Personal" ||
		!slices.Equal(res.ListsGone[0].Moved, want[0].Moved) || len(res.ListsGone[0].Lost)+len(res.ListsGone[0].Edits) != 0 {
		t.Errorf("gone = %+v, want %+v", res.ListsGone, want)
	}
	up := svc.todos[made[0].ID]
	if up == nil || up.DeletedAt != nil || up.ListID != "p1" || up.AssigneeID != nil || up.Title != "Book the ferry" {
		t.Errorf("the service has %+v, want the todo in the personal list with nobody assigned", up)
	}
	if got := get(t, b.Client(), made[0].ID); got.ListID != "p1" || got.AssigneeID != nil {
		t.Errorf("kept here: %+v, want the personal list", got)
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 || len(res.Refused) != 0 {
		t.Errorf("status = %+v, refused %v; the refusal was about the list it left", st, res.Refused)
	}
}

// The personal list is not the only place a person could be: the assignee
// stays when it is the person themselves.
func TestTheMovedTodoKeepsAnAssigneeWhoIsThePerson(t *testing.T) {
	ctx := context.Background()
	var made []api.Todo
	svc, _, _ := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		svc.down.Store(true)
		var err error
		made, err = c.CreateTodos(ctx, api.NewTodo{Title: "Book the ferry", ListID: "trip", AssigneeID: strp("u1")})
		if err != nil {
			t.Fatal(err)
		}
	})
	if up := svc.todos[made[0].ID]; up == nil || up.ListID != "p1" || up.AssigneeID == nil || *up.AssigneeID != "u1" {
		t.Errorf("the service has %+v, want the todo in the personal list, still assigned to the person", up)
	}
}

func TestATodoTheServiceHasWithUnsentEditsIsReportedWhenItsListLeaves(t *testing.T) {
	ctx := context.Background()
	var rent, quiet, removed api.Todo
	svc, b, res := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		rent = held(t, svc, b, "Pay rent", "trip")
		quiet = held(t, svc, b, "Nothing unsent", "trip")
		removed = held(t, svc, b, "Deleted here", "trip")
		svc.down.Store(true)
		if _, _, err := c.UpdateTodo(ctx, rent.ID, api.TodoPatch{Title: api.Val("Pay the rent")}); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteTodo(ctx, removed.ID); err != nil {
			t.Fatal(err)
		}
	})
	if len(res.ListsGone) != 1 || res.ListsGone[0].Name != "Trip" || !slices.Equal(res.ListsGone[0].Edits, []string{"Pay the rent"}) ||
		len(res.ListsGone[0].Moved)+len(res.ListsGone[0].Lost) != 0 {
		t.Errorf("gone = %+v, want only the edited todo reported", res.ListsGone)
	}
	for _, id := range []string{rent.ID, quiet.ID, removed.ID} {
		if _, err := b.Client().GetTodo(ctx, id); !api.IsNotFound(err) {
			t.Errorf("todo %s is still here: %v", id, err)
		}
		if svc.todos[id].DeletedAt == nil {
			t.Errorf("the service has todo %s alive", id)
		}
	}
	if st := b.Status(); st.Pending != 0 {
		t.Errorf("pending = %d, want nothing left to send", st.Pending)
	}
}

// A move into the list that never went up leaves the todo in the list the
// service has it in, where it stays, with its other changes.
func TestAMoveIntoAListThatLeftIsUndoneAndReported(t *testing.T) {
	ctx := context.Background()
	var todo api.Todo
	svc, b, res := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		todo = add(t, c, "Pay rent")
		mustSync(t, b)
		svc.down.Store(true)
		if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{ListID: api.Val("trip"), Title: api.Val("Pay the rent")}); err != nil {
			t.Fatal(err)
		}
	})
	if len(res.ListsGone) != 1 || res.ListsGone[0].Name != "Trip" || !slices.Equal(res.ListsGone[0].Edits, []string{"Pay the rent"}) {
		t.Errorf("gone = %+v, want the lost move reported", res.ListsGone)
	}
	got := get(t, b.Client(), todo.ID)
	if got.ListID != "p1" || got.Title != "Pay the rent" {
		t.Errorf("kept here: %+v, want it back in the personal list with the new title", got)
	}
	if up := svc.todos[todo.ID]; up.DeletedAt != nil || up.ListID != "p1" || up.Title != "Pay the rent" {
		t.Errorf("the service has %+v, want the other change to have gone up", up)
	}
	if st := b.Status(); st.Pending != 0 || st.Refused != 0 || len(res.Refused) != 0 {
		t.Errorf("status = %+v, refused %v", st, res.Refused)
	}
}

// With only the move to send, the todo is simply the service's again.
func TestAMoveAloneIntoAListThatLeftLeavesNothingToSend(t *testing.T) {
	ctx := context.Background()
	var todo api.Todo
	var before int
	svc, b, res := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		todo = add(t, c, "Pay rent")
		mustSync(t, b)
		before = svc.pushes(todo.ID)
		svc.down.Store(true)
		if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{ListID: api.Val("trip")}); err != nil {
			t.Fatal(err)
		}
	})
	if len(res.ListsGone) != 1 || !slices.Equal(res.ListsGone[0].Edits, []string{"Pay rent"}) {
		t.Errorf("gone = %+v", res.ListsGone)
	}
	if got := get(t, b.Client(), todo.ID); got.ListID != "p1" {
		t.Errorf("kept here: %+v, want the personal list", got)
	}
	if st := b.Status(); st.Pending != 0 || svc.pushes(todo.ID) != before+1 {
		t.Errorf("pending %d, pushes %d more; want the move tried once and nothing else sent", st.Pending, svc.pushes(todo.ID)-before)
	}
}

// With no Base to say where the service has the todo, it is treated as any
// other todo the account held.
func TestAMoveWithoutABaseGoesWithTheList(t *testing.T) {
	ctx := context.Background()
	var todo api.Todo
	_, b, res := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		todo = add(t, c, "Pay rent")
		mustSync(t, b)
		svc.down.Store(true)
		if _, _, err := c.UpdateTodo(ctx, todo.ID, api.TodoPatch{ListID: api.Val("trip")}); err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		for i := range b.st.Todos {
			b.st.Todos[i].Base = nil
		}
		b.mu.Unlock()
	})
	if len(res.ListsGone) != 1 || !slices.Equal(res.ListsGone[0].Edits, []string{"Pay rent"}) {
		t.Errorf("gone = %+v", res.ListsGone)
	}
	if _, err := b.Client().GetTodo(ctx, todo.ID); !api.IsNotFound(err) {
		t.Errorf("the todo is still here: %v", err)
	}
}

// Without a personal list to move them to, the todos made here are reported
// by title instead of going quietly.
func TestTodosMadeHereAreReportedWhenThereIsNoPersonalListToMoveThemTo(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "trip", Name: "Trip", Role: "owner"}, api.List{ID: "work", Name: "Work", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	if _, err := b.Client().CreateTodos(ctx, api.NewTodo{Title: "Book the ferry", ListID: "trip"}); err != nil {
		t.Fatal(err)
	}
	svc.dropList("trip")
	svc.lists = svc.lists[1:] // the snapshot has no personal list either
	res := mustSync(t, b)
	if len(res.ListsGone) != 1 || !slices.Equal(res.ListsGone[0].Lost, []string{"Book the ferry"}) || len(res.ListsGone[0].Moved) != 0 {
		t.Errorf("gone = %+v, want the todo reported as lost", res.ListsGone)
	}
}

// A list deleted here on purpose takes its todos without a word, as it
// always did: the person asked for that.
func TestAListDeletedHereReportsNothing(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "trip", Name: "Trip", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	rent := held(t, svc, b, "Pay rent", "trip")
	svc.down.Store(true)
	if _, _, err := c.UpdateTodo(ctx, rent.ID, api.TodoPatch{Title: api.Val("Pay the rent")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "Made in it", ListID: "trip"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteList(ctx, "trip"); err != nil {
		t.Fatal(err)
	}
	svc.down.Store(false)
	res := mustSync(t, b)
	if len(res.ListsGone) != 0 || len(res.Replaced) != 0 {
		t.Errorf("gone %+v, replaced %v, want nothing reported", res.ListsGone, res.Replaced)
	}
	if got := titles(t, c); len(got) != 0 {
		t.Errorf("todos here = %v, want none", got)
	}
}

// The first pull of an account prunes what the service does not hold, and
// must not take back the todo that was just moved to the personal list.
func TestAMovedTodoSurvivesTheFirstPullsPruning(t *testing.T) {
	ctx := context.Background()
	var made api.Todo
	svc, b, res := tripWith(t, func(svc *service, b *Backend, c *api.Client) {
		svc.down.Store(true)
		todos, err := c.CreateTodos(ctx, api.NewTodo{Title: "Book the ferry", ListID: "trip"})
		if err != nil {
			t.Fatal(err)
		}
		made = todos[0]
		// The next sync reads the account in full.
		b.mu.Lock()
		b.st.Cursor = ""
		b.mu.Unlock()
	})
	if len(res.ListsGone) != 1 || !slices.Equal(res.ListsGone[0].Moved, []string{"Book the ferry"}) {
		t.Errorf("gone = %+v", res.ListsGone)
	}
	if got := get(t, b.Client(), made.ID); got.ListID != "p1" {
		t.Errorf("kept here: %+v, want the personal list", got)
	}
	if up := svc.todos[made.ID]; up == nil || up.ListID != "p1" {
		t.Errorf("the service has %+v", up)
	}
}

// Every account has its personal list, so a snapshot with no lists at all
// is a broken answer, not every list leaving at once. It is ignored, on an
// incremental pull and on a full one, and nothing here goes.
func TestAnEmptyListSnapshotLeavesTheListsAndTodosHere(t *testing.T) {
	ctx := context.Background()
	svc, url := newService(t)
	svc.lists = append(svc.lists, api.List{ID: "work", Name: "Work", Role: "owner"})
	b := syncedAt(t, filepath.Join(t.TempDir(), "todos.json"), url)
	c := b.Client()
	add(t, c, "Buy milk")
	if _, err := c.CreateTodos(ctx, api.NewTodo{Title: "Write the report", ListID: "work"}); err != nil {
		t.Fatal(err)
	}
	mustSync(t, b)

	svc.noLists = true
	for _, full := range []bool{false, true} {
		if full {
			b.mu.Lock()
			b.st.Cursor = ""
			err := b.persist()
			b.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		}
		res := mustSync(t, b)
		if len(res.ListsGone) != 0 {
			t.Errorf("full %v: gone = %+v, want nothing", full, res.ListsGone)
		}
		if got := strings.Join(listNames(t, c), ","); got != "Personal,Work" {
			t.Errorf("full %v: lists = %s, want both kept", full, got)
		}
		if got := strings.Join(titles(t, c), ","); got != "Buy milk,Write the report" {
			t.Errorf("full %v: todos = %s, want both kept", full, got)
		}
	}
}
