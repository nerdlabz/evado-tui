package testapi

import (
	"context"
	"errors"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// The service answers 400 invalid_request with the field named when a
// timestamp has no UTC offset; the fake must too, or a client that forgets
// the offset would pass every test.
func TestPatchRefusesTimestampsWithoutAnOffset(t *testing.T) {
	f := New()
	c := f.Client(t)
	id := f.Todos[0].ID
	bad := []struct {
		path  string
		patch api.TodoPatch
	}{
		{"dueAt", api.TodoPatch{DueAt: api.Val("2026-03-10T15:00")}},
		{"remindAt", api.TodoPatch{RemindAt: api.Val("2026-03-10T15:00:00")}},
		{"doAt", api.TodoPatch{DoAt: api.Val("2026-03-10")}},
		{"scheduledDay", api.TodoPatch{ScheduledDay: api.Val("2026-03-10T15:00:00Z")}},
	}
	for _, b := range bad {
		_, _, err := c.UpdateTodo(context.Background(), id, b.patch)
		var e *api.Error
		if !errors.As(err, &e) || e.Code != api.CodeInvalidRequest || len(e.Fields) != 1 ||
			e.Fields[0].Path != b.path || e.Fields[0].Message != "Invalid datetime" {
			t.Errorf("%s: got %v", b.path, err)
		}
	}
	good := []api.TodoPatch{
		{DueAt: api.Val("2026-03-10T15:00:00Z")},
		{DoAt: api.Val("2026-03-10T00:00:00+01:00"), HasTime: api.BoolVal(false)},
		{DueAt: api.Null, ScheduledDay: api.Val("2026-03-10")},
	}
	for _, p := range good {
		if _, _, err := c.UpdateTodo(context.Background(), id, p); err != nil {
			t.Errorf("%v: %v", p, err)
		}
	}
}

// Creating a list with an id of its own is safe to send again, and refuses
// an id it may not use or one of a deleted list, as the service does.
func TestCreatingAListWithAnIDIsSafeToRepeat(t *testing.T) {
	f := New()
	f.ForeignLists = []string{"theirs"}
	f.DeletedLists = []string{"gone"}
	c := f.Client(t)
	ctx := context.Background()

	made, err := c.CreateListWithID(ctx, "mine", "Trip")
	if err != nil || made.ID != "mine" || made.Name != "Trip" {
		t.Fatalf("first create = %+v, %v", made, err)
	}
	again, err := c.CreateListWithID(ctx, "mine", "Holiday")
	if err != nil || again.ID != "mine" || again.Name != "Trip" || len(f.Lists) != 3 {
		t.Errorf("repeat = %+v, %v, %d lists; want the same list, unrenamed", again, err, len(f.Lists))
	}
	for id, code := range map[string]string{"theirs": api.CodeIDConflict, "l1": api.CodeIDConflict, "gone": api.CodeListDeleted} {
		if _, err := c.CreateListWithID(ctx, id, "Trip"); api.CodeOf(err) != code {
			t.Errorf("create with %s = %v, want %s", id, err, code)
		}
	}
	if none, err := c.CreateList(ctx, "Trip"); err != nil || none.ID == "" || none.ID == "mine" {
		t.Errorf("create without an id = %+v, %v, want a new id", none, err)
	}
	if len(f.Lists) != 4 {
		t.Errorf("lists = %+v, want one made with the id and one without", f.Lists)
	}
}
