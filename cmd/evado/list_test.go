package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/local"
	"github.com/nerdlabz/evado-tui/internal/ops"
	"github.com/nerdlabz/evado-tui/internal/testapi"
)

// runNoting is run with the standard error the command wrote beside its
// result.
func runNoting(t *testing.T, f *testapi.API, args ...string) (out, errw string, err error) {
	t.Helper()
	var o, e bytes.Buffer
	err = runCommandNoting(context.Background(), ops.New(f.Client(t)), args, &o, &e, nil, wantsJSON(args, ""))
	return o.String(), e.String(), err
}

func onlyTodos(f *testapi.API, n int) {
	f.Todos = nil
	for i := range n {
		f.Todos = append(f.Todos, api.Todo{ID: fmt.Sprintf("t%04d", i), ListID: "l1", Title: fmt.Sprintf("todo %d", i)})
	}
}

// ls -json used to be {"count", "todos"}; scripts read those two keys, so
// the cursor is a third beside them, null when nothing more matches.
func TestListJSONKeepsItsShapeAndAddsNextCursor(t *testing.T) {
	f := testapi.New()
	out, _, err := runNoting(t, f, "ls", "-json")
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys["count"] == nil || keys["todos"] == nil || string(keys["nextCursor"]) != "null" {
		t.Errorf("keys = %v, want count, todos and a null nextCursor", keys)
	}

	onlyTodos(f, 450)
	out, _, err = runNoting(t, f, "ls", "-json")
	var page struct {
		Count      int
		Todos      []api.Todo
		NextCursor *string
	}
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || page.Count != 200 || page.NextCursor == nil {
		t.Fatalf("first 200 of 450: %v %q", err, out)
	}
}

func TestListLimitGoesPastTwoHundredAndSaysWhenMoreMatch(t *testing.T) {
	f := testapi.New()
	onlyTodos(f, 450)

	out, note, err := runNoting(t, f, "ls", "-limit", "450")
	if err != nil || strings.Count(out, "\n") != 450 || note != "" {
		t.Fatalf("limit 450: %d lines, note %q, %v; want every todo and no note", strings.Count(out, "\n"), note, err)
	}

	out, note, err = runNoting(t, f, "ls", "-limit", "300")
	if err != nil || strings.Count(out, "\n") != 300 {
		t.Fatalf("limit 300: %d lines, %v", strings.Count(out, "\n"), err)
	}
	if !strings.HasPrefix(note, "evado: 300 shown and more match") || !strings.Contains(note, "-cursor ") {
		t.Errorf("note = %q, want it to say more match and how to carry on", note)
	}
	if strings.Contains(out, "evado:") {
		t.Errorf("the note leaked into the list: %q", out)
	}
}

func TestListCursorCarriesOnAfterTheLastTodoPrinted(t *testing.T) {
	f := testapi.New()
	onlyTodos(f, 450)

	out, _, err := runNoting(t, f, "ls", "-limit", "250", "-json", "-full-ids")
	var first struct {
		Todos      []api.Todo
		NextCursor string
	}
	if err != nil || json.Unmarshal([]byte(out), &first) != nil || first.NextCursor == "" {
		t.Fatalf("first: %v %q", err, out)
	}
	out, _, err = runNoting(t, f, "ls", "-limit", "250", "-cursor", first.NextCursor, "-json", "-full-ids")
	var rest struct {
		Todos      []api.Todo
		NextCursor *string
	}
	if err != nil || json.Unmarshal([]byte(out), &rest) != nil || len(rest.Todos) != 200 || rest.NextCursor != nil {
		t.Fatalf("rest: %v %q", err, out)
	}
	if first.Todos[249].ID != "t0249" || rest.Todos[0].ID != "t0250" {
		t.Errorf("seam = %s then %s, want t0249 then t0250", first.Todos[249].ID, rest.Todos[0].ID)
	}
}

func TestListMalformedCursorIsRefusedByTheService(t *testing.T) {
	_, _, err := runNoting(t, testapi.New(), "ls", "-cursor", "yesterday")
	if info := describe(err); info.ExitCode != ops.ExitRejected {
		t.Errorf("info = %+v, want the service's refusal, exit %d", info, ops.ExitRejected)
	}
}

// The todos on this computer answer the same way: a cursor in -json, a note
// beside the text, and a cursor that carries on.
func TestListPagesOnThisComputerToo(t *testing.T) {
	b, err := local.Open(filepath.Join(t.TempDir(), "todos.json"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	c := b.Client()
	for range 12 {
		lines := make([]string, 20)
		for i := range lines {
			lines[i] = "todo"
		}
		if _, err := c.CreateTodos(context.Background(), api.NewTodo{Title: strings.Join(lines, "\n")}); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, string) {
		var out, note bytes.Buffer
		if err := runCommandNoting(context.Background(), ops.New(c), args, &out, &note, nil, wantsJSON(args, "")); err != nil {
			t.Fatal(err)
		}
		return out.String(), note.String()
	}

	out, note := run("ls")
	if strings.Count(out, "\n") != 200 || !strings.Contains(note, "more match") {
		t.Fatalf("ls: %d lines, note %q, want 200 and a note", strings.Count(out, "\n"), note)
	}
	out, _ = run("ls", "-json", "-limit", "1000")
	var all struct {
		Count      int
		NextCursor *string
	}
	if err := json.Unmarshal([]byte(out), &all); err != nil || all.Count != 240 || all.NextCursor != nil {
		t.Fatalf("ls -limit 1000: %v %q, want all 240 and a null cursor", err, out)
	}
	out, _ = run("ls", "-json", "-limit", "100")
	var first struct{ NextCursor string }
	if err := json.Unmarshal([]byte(out), &first); err != nil || first.NextCursor == "" {
		t.Fatalf("first 100: %v %q", err, out)
	}
	out, _ = run("ls", "-json", "-limit", "1000", "-cursor", first.NextCursor)
	var rest struct{ Count int }
	if err := json.Unmarshal([]byte(out), &rest); err != nil || rest.Count != 140 {
		t.Errorf("after the first 100: %v %q, want the other 140", err, out)
	}
}

// A negative limit is refused like any other bad number, rather than read
// as the default.
func TestListRefusesANegativeLimit(t *testing.T) {
	f := testapi.New()
	onlyTodos(f, 3)
	_, err := run(t, f, "", "ls", "-limit", "-5")
	if describe(err).ExitCode != ops.ExitUsage || !strings.Contains(err.Error(), "-limit must be 0 or more") {
		t.Fatalf("ls -limit -5: %v, want a usage error", err)
	}
	if out, err := run(t, f, "", "ls", "-limit", "0"); err != nil || strings.Count(out, "\n") != 3 {
		t.Errorf("ls -limit 0: %q %v, want the default", out, err)
	}
}
