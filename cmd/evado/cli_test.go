package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/ops"
	"github.com/nerdlabz/evado-tui/internal/testapi"
)

func run(t *testing.T, f *testapi.API, in string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	var stdin io.Reader // nil, like a terminal
	if in != "" {
		stdin = strings.NewReader(in)
	}
	o := ops.New(f.Client(t))
	err := runCommand(context.Background(), o, args, &out, stdin, wantsJSON(args, ""))
	return out.String(), err
}

func TestIsCommand(t *testing.T) {
	for _, a := range []string{"add", "ls", "show", "done", "undone", "edit", "claim", "release", "rm", "step", "steps", "changes", "lists", "parse"} {
		if !isCommand([]string{a}) {
			t.Errorf("%q should be a command", a)
		}
	}
	if isCommand(nil) || isCommand([]string{"bogus"}) {
		t.Error("nothing and unknown words are not commands")
	}
}

func TestWantsJSON(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{"off by default", []string{"ls"}, "", false},
		{"-json", []string{"ls", "-json"}, "", true},
		{"--json", []string{"ls", "--json"}, "", true},
		{"-json=true", []string{"ls", "-json=true"}, "", true},
		{"--json=1", []string{"ls", "--json=1"}, "", true},
		{"after the id", []string{"show", "aaa111", "-json=true"}, "", true},
		{"on a steps command", []string{"steps", "done", "aaa111", "s1", "-json=t"}, "", true},
		{"on import", []string{"import", "-json=true", "-"}, "", true},
		{"EVADO_JSON=1", []string{"ls"}, "1", true},
		{"EVADO_JSON=true", []string{"ls"}, "true", true},
		{"-json=false beats EVADO_JSON", []string{"ls", "-json=false"}, "1", false},
		{"the last one wins", []string{"ls", "-json", "-json=false"}, "", false},
		{"after -- it is text", []string{"add", "--", "-json"}, "", false},
		{"the value of another flag", []string{"edit", "aaa111", "-notes", "-json"}, "", false},
		{"after a flag that does not parse", []string{"ls", "-limt", "5", "-json"}, "", true},
		{"on an unknown steps command", []string{"steps", "bogus", "-json=true"}, "", true},
	} {
		if got := wantsJSON(c.args, c.env); got != c.want {
			t.Errorf("%s: wantsJSON(%q, %q) = %v, want %v", c.name, c.args, c.env, got, c.want)
		}
	}
	// Each command is run to read its flags, with no account behind it, so
	// each must stop at its flags.
	for _, name := range commandNames {
		args := []string{name, "x", "y", "-json=true"}
		if name == "steps" {
			args = []string{name, "list", "x", "-json=true"}
		}
		if !wantsJSON(args, "") {
			t.Errorf("%s -json=true: wantsJSON = false", name)
		}
	}
}

// The output follows the same -json the error printing does, however it is
// spelled, and a -json that is another flag's value is only that value.
func TestJSONFlagIsReadAsTheFlagPackageReadsIt(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "", "ls", "-json=true")
	if err != nil || !json.Valid([]byte(out)) {
		t.Errorf("ls -json=true printed %q %v, want JSON", out, err)
	}
	args := []string{"ls", "-json=false"}
	var b bytes.Buffer
	err = runCommand(context.Background(), ops.New(f.Client(t)), args, &b, nil, wantsJSON(args, "1"))
	if err != nil || !strings.Contains(b.String(), "aaa111  ○") {
		t.Errorf("EVADO_JSON=1 ls -json=false printed %q %v, want text", b.String(), err)
	}
	out, err = run(t, f, "", "edit", "aaa111", "-notes", "-json")
	if err != nil || !strings.HasPrefix(out, "saved aaa111") {
		t.Errorf("edit -notes -json printed %q %v, want text", out, err)
	}
	if p := f.Patches[len(f.Patches)-1]; string(p["notes"]) != `"-json"` {
		t.Errorf("patch = %v, want the notes set to the literal -json", p)
	}
}

func TestAddJoinsWordsAndPicksTheList(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "", "add", "-list", "work", "Call", "the", "bank", "tomorrow")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "added new001  Call the bank tomorrow") {
		t.Errorf("output = %q", out)
	}
	if got := f.Find("new001"); got == nil || got.ListID != "l2" {
		t.Errorf("stored = %+v, want it on the Work list", got)
	}
}

func TestAddReadsStandardInput(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "one\ntwo\n", "add", "-")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "added") != 2 {
		t.Errorf("output = %q, want two todos from two lines", out)
	}
	// With nothing piped and nothing typed there is nothing to add.
	if _, err := run(t, f, "", "add"); describe(err).ExitCode != ops.ExitUsage {
		t.Errorf("add with no text should be a usage error, got %v", err)
	}
}

// lines returns n numbered todo lines, long enough together to be well
// past the 2000 characters the service takes in one title.
func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "Smoke todo %d\n", i)
	}
	return b.String()
}

func TestAddSendsLongInputInBatches(t *testing.T) {
	f := testapi.New()
	before := len(f.Todos)
	out, err := run(t, f, lines(250), "add", "-")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "added"); got != 250 {
		t.Fatalf("added %d todos, want 250", got)
	}
	if f.Creates < 2 {
		t.Errorf("%d create requests; 250 lines do not fit in one title", f.Creates)
	}
	made := f.Todos[before:]
	for i, todo := range made {
		if want := fmt.Sprintf("Smoke todo %d", i+1); todo.Title != want {
			t.Fatalf("todo %d is %q, want %q: the lines lost their order", i, todo.Title, want)
		}
	}
}

func TestAddKeepsEveryLineOfShortInput(t *testing.T) {
	// 30 short lines fit in one title, but the service makes todos of only
	// the first 20 lines of a title and drops the rest.
	f := testapi.New()
	out, err := run(t, f, lines(30), "add", "-")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "added"); got != 30 {
		t.Errorf("added %d todos, want 30", got)
	}
}

func TestAddRefusesALineTooLongForATodo(t *testing.T) {
	f := testapi.New()
	_, err := run(t, f, "short\n"+strings.Repeat("x", 2001)+"\n", "add", "-")
	if describe(err).ExitCode != ops.ExitUsage || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want a usage error naming line 2", err)
	}
	if f.Creates != 0 {
		t.Errorf("%d create requests; nothing should be sent when a line cannot be a todo", f.Creates)
	}
}

func TestAddReportsTheTodosMadeBeforeABatchFails(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		f := testapi.New()
		f.CreateError = func(n int) *api.Error {
			if n == 2 {
				return &api.Error{StatusCode: http.StatusServiceUnavailable, Code: api.CodeUpstreamFailed}
			}
			return nil
		}
		args := []string{"add", "-"}
		if asJSON {
			args = append(args, "-json")
		}
		out, err := run(t, f, lines(250), args...)
		if err == nil || describe(err).ExitCode != ops.ExitServer {
			t.Fatalf("json=%v: err = %v, want a server error", asJSON, err)
		}
		made := len(f.Todos) - len(testapi.New().Todos)
		if made == 0 || made == 250 {
			t.Fatalf("json=%v: %d todos made; the first batch should be in and the rest not", asJSON, made)
		}
		if asJSON {
			var got struct{ Todos []api.Todo }
			if jerr := json.Unmarshal([]byte(out), &got); jerr != nil || len(got.Todos) != made {
				t.Errorf("json=%v: printed %d todos (%v), want the %d made", asJSON, len(got.Todos), jerr, made)
			}
		} else if got := strings.Count(out, "added"); got != made {
			t.Errorf("json=%v: printed %d added lines, want the %d made", asJSON, got, made)
		}
	}
}

func TestAddJSON(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "", "add", "-json", "-notes", "n", "Buy tea")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Todos []api.Todo }
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Todos) != 1 || got.Todos[0].Title != "Buy tea" {
		t.Errorf("json = %v %q", err, out)
	}
}

func TestListTextAndJSON(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "aaa111  ○  Pay the cable bill  (2026-03-10)") || strings.Contains(out, "Buy milk") {
		t.Errorf("output = %q, want the open todos only", out)
	}
	out, err = run(t, f, "", "ls", "-status", "completed", "-json", "-full-ids")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Count int
		Todos []api.Todo
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Count != 1 || got.Todos[0].ID != "bbb333" {
		t.Errorf("json = %v %q", err, out)
	}
	f.Todos = nil
	out, _ = run(t, f, "", "ls", "-json")
	if !strings.Contains(out, `"todos": []`) {
		t.Errorf("an empty listing should be [] not null: %q", out)
	}
}

func TestBadStatusIsAUsageError(t *testing.T) {
	_, err := run(t, testapi.New(), "", "ls", "-status", "sometime")
	info := describe(err)
	if info.ExitCode != ops.ExitUsage || !strings.Contains(info.Message, "today") {
		t.Errorf("info = %+v", info)
	}
}

func TestFlagsMayFollowThePositionalArguments(t *testing.T) {
	f := testapi.New()
	if _, err := run(t, f, "", "edit", "aaa222", "-title", "Renew passport now"); err != nil {
		t.Fatal(err)
	}
	if got := f.Find("aaa222").Title; got != "Renew passport now" {
		t.Errorf("title = %q", got)
	}
	// After -- everything is text, even something that looks like a flag.
	out, err := run(t, f, "", "add", "--", "-json is a flag")
	if err != nil || !strings.Contains(out, "-json is a flag") {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestEditSetsOnlyWhatWasGiven(t *testing.T) {
	f := testapi.New()
	if _, err := run(t, f, "", "edit", "aaa111", "-notes", "none", "-day", "2026-05-01", "-done=true"); err != nil {
		t.Fatal(err)
	}
	p := f.Patches[len(f.Patches)-1]
	if string(p["notes"]) != "null" || string(p["scheduledDay"]) != `"2026-05-01"` || string(p["isCompleted"]) != "true" {
		t.Errorf("patch = %v", p)
	}
	if _, touched := p["title"]; touched {
		t.Error("the title was not given and must not be sent")
	}
	if _, err := run(t, f, "", "edit", "aaa111"); describe(err).ExitCode != ops.ExitUsage {
		t.Errorf("an edit with no changes should be a usage error, got %v", err)
	}
}

func TestShowPrintsStepsAndJSON(t *testing.T) {
	f := testapi.New()
	if _, err := run(t, f, "", "step", "aaa111", "call", "them"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, f, "", "show", "aaa111")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Pay the cable bill", "list      Personal", "□ call them"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
	out, _ = run(t, f, "", "show", "-json", "aaa111")
	var d ops.Detail
	if err := json.Unmarshal([]byte(out), &d); err != nil || len(d.Steps) != 1 || d.ListName != "Personal" {
		t.Errorf("json = %v %+v", err, d)
	}
}

func TestDoneHandlesSeveralAndReportsTheFirstFailure(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "", "done", "aaa111", "zzz", "aaa222")
	if describe(err).ExitCode != ops.ExitNotFound {
		t.Fatalf("err = %v, want the not-found failure", err)
	}
	if !f.Find("aaa111").IsCompleted || !f.Find("aaa222").IsCompleted {
		t.Error("the good ids should still be completed")
	}
	if strings.Contains(out, "zzz") {
		t.Errorf("the failure belongs on standard error, not in the results: %q", out)
	}
	if !strings.Contains(out, "done aaa111") || !strings.Contains(out, "done aaa222") {
		t.Errorf("the good ids should be reported: %q", out)
	}
	out, err = run(t, f, "", "done", "-json", "aaa111")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Results []ops.Change }
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Results) != 1 || got.Results[0].Changed {
		t.Errorf("completing again should report no change: %v %q", err, out)
	}
	out, _ = run(t, f, "", "undone", "aaa111")
	if !strings.Contains(out, "reopened") || f.Find("aaa111").IsCompleted {
		t.Errorf("undone: %q", out)
	}
}

func TestRemoveNeedsYes(t *testing.T) {
	f := testapi.New()
	_, err := run(t, f, "", "rm", "aaa111")
	if describe(err).ExitCode != ops.ExitUsage {
		t.Errorf("rm without -yes: %v", err)
	}
	if f.Find("aaa111") == nil {
		t.Fatal("nothing should be deleted without -yes")
	}
	out, err := run(t, f, "", "rm", "-yes", "aaa111")
	if err != nil || !strings.Contains(out, "deleted aaa111") || f.Find("aaa111") != nil {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestListsAndParse(t *testing.T) {
	f := testapi.New()
	out, err := run(t, f, "", "lists")
	if err != nil || !strings.Contains(out, "Personal") || !strings.Contains(out, "personal") || !strings.Contains(out, "Work") {
		t.Errorf("lists: %q %v", out, err)
	}
	out, err = run(t, f, "", "parse", "call", "mum", "tomorrow")
	if err != nil || !strings.Contains(out, "no date") {
		t.Errorf("parse: %q %v", out, err)
	}
	if _, err := run(t, f, "", "parse"); describe(err).ExitCode != ops.ExitUsage {
		t.Errorf("parse with no text should be a usage error")
	}
}

func TestParseSaysWhenATimeWasAssumed(t *testing.T) {
	f := testapi.New()
	at := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	f.Parsed = api.ParsePreview{
		Title: "Call", DoAt: &at, HasTime: true,
		AssumedTime: &api.AssumedTime{Text: "at 9", Assumed: "am", AlternativeText: "at 9pm", AlternativeDoAt: at.Add(12 * time.Hour)},
	}
	out, err := run(t, f, "", "parse", "call", "at", "9")
	if err != nil || !strings.Contains(out, `assumed "at 9" was read as AM; type 9pm for evening`) {
		t.Errorf("parse: %q %v", out, err)
	}
	out, err = run(t, f, "", "parse", "-json", "call", "at", "9")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Parse struct {
			AssumedTime *api.AssumedTime `json:"assumedTime"`
		} `json:"parse"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Parse.AssumedTime == nil || got.Parse.AssumedTime.AlternativeText != "at 9pm" {
		t.Errorf("parse -json: %q %v", out, err)
	}

	f.Parsed.AssumedTime = nil
	out, err = run(t, f, "", "parse", "call", "at", "9pm")
	if err != nil || strings.Contains(out, "assumed") {
		t.Errorf("parse of a time that says which half: %q %v", out, err)
	}
	out, _ = run(t, f, "", "parse", "-json", "call", "at", "9pm")
	if !strings.Contains(out, `"assumedTime": null`) {
		t.Errorf("parse -json should carry assumedTime null: %q", out)
	}
}

func TestErrorTextNamesCandidates(t *testing.T) {
	_, err := run(t, testapi.New(), "", "show", "aaa")
	text := errorText(err)
	if !strings.Contains(text, "aaa111") || !strings.Contains(text, "aaa222") {
		t.Errorf("an ambiguous id should list what it matched: %q", text)
	}
	if describe(err).ExitCode != ops.ExitUsage {
		t.Errorf("exit = %d, want usage", describe(err).ExitCode)
	}
}

func TestSeveralFailuresAreAllNamed(t *testing.T) {
	f := testapi.New()
	_, err := run(t, f, "", "done", "zzz", "yyy")
	text := errorText(err)
	if !strings.Contains(text, "zzz:") || !strings.Contains(text, "yyy:") {
		t.Errorf("both failures should be named: %q", text)
	}
	if describe(err).ExitCode != ops.ExitNotFound {
		t.Errorf("exit = %d, want the first failure's code", describe(err).ExitCode)
	}
	out, err := run(t, f, "", "done", "-json", "aaa111", "zzz")
	if err == nil {
		t.Fatal("expected the failure to be returned")
	}
	var got struct {
		Results []ops.Change
		Errors  []map[string]any
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil || len(got.Results) != 1 || len(got.Errors) != 1 || got.Errors[0]["ref"] != "zzz" {
		t.Errorf("json = %v %q", jerr, out)
	}
}

// stepsFixture has a checklist with two steps whose ids share a long prefix,
// another todo's step, and a todo on a read-only shared list.
func stepsFixture() *testapi.API {
	f := testapi.New()
	f.Lists = append(f.Lists, api.List{ID: "l3", Name: "Shared", ReadOnly: true})
	f.Todos = append(f.Todos, api.Todo{ID: "ccc444", ListID: "l3", Title: "Read only chore"})
	f.Steps["aaa111"] = []api.ChecklistItem{
		{ID: "abcd1234-0001", TodoID: "aaa111", Title: "first"},
		{ID: "abcd1234-0002", TodoID: "aaa111", Title: "second"},
		{ID: "ffff0000-0003", TodoID: "aaa111", Title: "third"},
	}
	f.Steps["aaa222"] = []api.ChecklistItem{{ID: "zzzz9999-0004", TodoID: "aaa222", Title: "elsewhere"}}
	f.Steps["ccc444"] = []api.ChecklistItem{{ID: "rrrr5555-0005", TodoID: "ccc444", Title: "locked"}}
	return f
}

func TestStepsListShowsUsableIds(t *testing.T) {
	f := stepsFixture()
	out, err := run(t, f, "", "steps", "list", "aaa111")
	if err != nil {
		t.Fatal(err)
	}
	// Two ids share their first eight characters, so the short form has to
	// grow until each can be told apart.
	for _, want := range []string{"abcd1234-0001  □  first", "abcd1234-0002  □  second", "ffff0000  □  third"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	out, err = run(t, f, "", "steps", "list", "-full-ids", "aaa111")
	if err != nil || !strings.Contains(out, "ffff0000-0003  □  third") {
		t.Errorf("full ids: %q %v", out, err)
	}
	out, err = run(t, f, "", "steps", "list", "-json", "aaa111")
	var got struct {
		Todo  api.Todo
		Count int
		Steps []api.ChecklistItem
	}
	if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil || got.Todo.ID != "aaa111" || got.Count != 3 || got.Steps[2].ID != "ffff0000-0003" {
		t.Errorf("json = %v %v %q", err, jerr, out)
	}
	out, _ = run(t, f, "", "steps", "list", "-json", "bbb333")
	if !strings.Contains(out, `"steps": []`) {
		t.Errorf("no steps should be [] not null: %q", out)
	}
}

func TestStepsAddAndTheStepShorthand(t *testing.T) {
	f := stepsFixture()
	out, err := run(t, f, "", "steps", "add", "aaa222", "buy", "stamps")
	if err != nil || !strings.Contains(out, "added step  step001  buy stamps") {
		t.Errorf("steps add: %q %v", out, err)
	}
	out, err = run(t, f, "", "step", "-json", "aaa222", "post", "it")
	var got struct{ Step api.ChecklistItem }
	if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil || got.Step.ID != "step002" || got.Step.TodoID != "aaa222" || got.Step.Title != "post it" {
		t.Errorf("step shorthand json = %v %v %q", err, jerr, out)
	}
	if n := len(f.Steps["aaa222"]); n != 3 {
		t.Errorf("aaa222 has %d steps, want 3", n)
	}
	if _, err := run(t, f, "", "steps", "add", "aaa222"); describe(err).ExitCode != ops.ExitUsage {
		t.Errorf("add with no text: %v", err)
	}
}

func TestStepsDoneAndUndoneSetTheState(t *testing.T) {
	f := stepsFixture()
	out, err := run(t, f, "", "steps", "done", "aaa111", "ffff")
	if err != nil || !strings.Contains(out, "done step  ffff0000  third") || f.Steps["aaa111"][2].CompletedAt == nil {
		t.Fatalf("done: %q %v", out, err)
	}
	// Doing it again must leave it done, not flip it back.
	out, err = run(t, f, "", "steps", "done", "-json", "aaa111", "ffff0000-0003")
	var got ops.StepChange
	if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil || got.Changed || got.Step.CompletedAt == nil || got.Step.ID != "ffff0000-0003" {
		t.Errorf("repeated done = %v %v %q", err, jerr, out)
	}
	if f.Steps["aaa111"][2].CompletedAt == nil {
		t.Error("a repeated done toggled the step")
	}
	out, err = run(t, f, "", "steps", "undone", "aaa111", "ffff")
	if err != nil || !strings.Contains(out, "reopened step") || f.Steps["aaa111"][2].CompletedAt != nil {
		t.Errorf("undone: %q %v", out, err)
	}
	out, err = run(t, f, "", "steps", "undone", "aaa111", "ffff")
	if err != nil || !strings.Contains(out, "already reopened step") || f.Steps["aaa111"][2].CompletedAt != nil {
		t.Errorf("repeated undone: %q %v", out, err)
	}
}

func TestStepsEditRenames(t *testing.T) {
	f := stepsFixture()
	out, err := run(t, f, "", "steps", "edit", "aaa111", "ffff", "third", "and", "last")
	if err != nil || !strings.Contains(out, "saved step  ffff0000  third and last") || f.Steps["aaa111"][2].Title != "third and last" {
		t.Fatalf("edit: %q %v", out, err)
	}
	out, err = run(t, f, "", "steps", "edit", "-json", "aaa111", "ffff", "-title", "renamed")
	var got ops.StepChange
	if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil || got.Step.Title != "renamed" || !got.Changed {
		t.Errorf("edit -title json = %v %v %q", err, jerr, out)
	}
	for _, args := range [][]string{
		{"steps", "edit", "aaa111", "ffff"},
		{"steps", "edit", "aaa111", "ffff", "x", "-title", "y"},
		{"steps", "edit", "aaa111", "ffff", "-title", " "},
	} {
		if _, err := run(t, f, "", args...); describe(err).ExitCode != ops.ExitUsage {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
	if f.Steps["aaa111"][2].Title != "renamed" {
		t.Error("a refused edit changed the title")
	}
}

func TestStepsRemoveNeedsYes(t *testing.T) {
	f := stepsFixture()
	_, err := run(t, f, "", "steps", "rm", "aaa111", "ffff")
	if describe(err).ExitCode != ops.ExitUsage || len(f.Steps["aaa111"]) != 3 {
		t.Fatalf("rm without -yes: %v, %d steps left", err, len(f.Steps["aaa111"]))
	}
	out, err := run(t, f, "", "steps", "rm", "-yes", "aaa111", "ffff")
	if err != nil || !strings.Contains(out, "deleted step  ffff0000  third") || len(f.Steps["aaa111"]) != 2 {
		t.Errorf("rm -yes: %q %v", out, err)
	}
	out, err = run(t, f, "", "steps", "rm", "-yes", "-json", "aaa111", "abcd1234-0001")
	if err != nil || !strings.Contains(out, `"id": "abcd1234-0001"`) || !strings.Contains(out, `"todoId": "aaa111"`) {
		t.Errorf("rm -json: %q %v", out, err)
	}
}

func TestStepsRefuseAStepOfAnotherTodo(t *testing.T) {
	f := stepsFixture()
	for _, sub := range []string{"done", "undone", "rm", "edit"} {
		args := []string{"steps", sub, "aaa111", "zzzz9999-0004"}
		if sub == "rm" {
			args = append(args, "-yes")
		}
		if sub == "edit" {
			args = append(args, "hijacked")
		}
		_, err := run(t, f, "", args...)
		if describe(err).ExitCode != ops.ExitNotFound {
			t.Errorf("%s with another todo's step: %v, want not found", sub, err)
		}
	}
	other := f.Steps["aaa222"][0]
	if other.Title != "elsewhere" || other.CompletedAt != nil || len(f.Steps["aaa222"]) != 1 {
		t.Errorf("the other todo's step was touched: %+v", f.Steps["aaa222"])
	}
}

func TestStepsMissingAndAmbiguousReferences(t *testing.T) {
	f := stepsFixture()
	_, err := run(t, f, "", "steps", "done", "aaa111", "nope")
	if describe(err).ExitCode != ops.ExitNotFound {
		t.Errorf("missing step: %v", err)
	}
	// A todo with no steps at all is the same kind of miss.
	if _, err := run(t, f, "", "steps", "done", "bbb333", "s"); describe(err).ExitCode != ops.ExitNotFound {
		t.Errorf("no checklist: %v", err)
	}
	_, err = run(t, f, "", "steps", "done", "aaa111", "abcd")
	info := describe(err)
	text := errorText(err)
	if info.ExitCode != ops.ExitUsage || !strings.Contains(text, "abcd1234-0001") || !strings.Contains(text, "abcd1234-0002") {
		t.Errorf("an ambiguous step should list its candidates: %q", text)
	}
	if f.Steps["aaa111"][0].CompletedAt != nil || f.Steps["aaa111"][1].CompletedAt != nil {
		t.Error("an ambiguous reference changed something")
	}
	// A full id is never ambiguous, even when it is also the start of a longer one.
	if _, err := run(t, f, "", "steps", "done", "aaa111", "abcd1234-0002"); err != nil {
		t.Errorf("full id: %v", err)
	}
	// The todo reference is resolved as strictly as everywhere else.
	_, err = run(t, f, "", "steps", "done", "aaa", "abcd1234-0001")
	text = errorText(err)
	if describe(err).ExitCode != ops.ExitUsage || !strings.Contains(text, "aaa111") || !strings.Contains(text, "aaa222") {
		t.Errorf("an ambiguous todo should list its candidates: %q", text)
	}
	if _, err := run(t, f, "", "steps", "done", "zzz", "abcd1234-0001"); describe(err).ExitCode != ops.ExitNotFound {
		t.Errorf("missing todo: %v", err)
	}
}

func TestStepsOnAReadOnlyListOnlyAllowCompletion(t *testing.T) {
	f := stepsFixture()
	if _, err := run(t, f, "", "steps", "done", "ccc444", "rrrr"); err != nil || f.Steps["ccc444"][0].CompletedAt == nil {
		t.Fatalf("ticking on a read-only list should work: %v", err)
	}
	if _, err := run(t, f, "", "steps", "undone", "ccc444", "rrrr"); err != nil || f.Steps["ccc444"][0].CompletedAt != nil {
		t.Fatalf("unticking on a read-only list should work: %v", err)
	}
	for _, args := range [][]string{
		{"steps", "edit", "ccc444", "rrrr", "new"},
		{"steps", "rm", "-yes", "ccc444", "rrrr"},
		{"steps", "add", "ccc444", "more"},
		{"step", "ccc444", "more"},
	} {
		_, err := run(t, f, "", args...)
		info := describe(err)
		if info.ExitCode != ops.ExitRejected || info.Code != api.CodeListReadOnly {
			t.Errorf("%v: %+v, want the server's refusal with exit 6", args, info)
		}
	}
	if s := f.Steps["ccc444"]; len(s) != 1 || s[0].Title != "locked" {
		t.Errorf("the read-only checklist changed: %+v", s)
	}
}

func TestStepsUsageErrors(t *testing.T) {
	f := stepsFixture()
	for _, args := range [][]string{
		{"steps"},
		{"steps", "bogus"},
		{"steps", "list"},
		{"steps", "list", "aaa111", "extra"},
		{"steps", "done", "aaa111"},
		{"steps", "done", "aaa111", "a", "b"},
		{"step", "aaa111"},
	} {
		if _, err := run(t, f, "", args...); describe(err).ExitCode != ops.ExitUsage {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
}

// feedFixture has n todos that changed one second apart, starting just after
// feedStart, and nothing else.
func feedFixture(n int) *testapi.API {
	f := testapi.New()
	f.Todos = nil
	for i := 1; i <= n; i++ {
		f.Todos = append(f.Todos, api.Todo{ID: fmt.Sprintf("c%04d", i), ListID: "l1", Title: fmt.Sprintf("todo %d", i)})
	}
	return f
}

// cur is the cursor the fake gives for todo i (counting from 1) having
// changed; cur(0) is the start of its feed. A client treats it as opaque.
func cur(i int) string { return testapi.Cursor(feedAt(i)) }

var feedStart = cur(0)

// feedAt is when the fake says todo i (counting from 1) changed.
func feedAt(i int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)
}

func changes(t *testing.T, f *testapi.API, args ...string) (ops.Changes, error) {
	t.Helper()
	out, err := run(t, f, "", append([]string{"changes", "-json"}, args...)...)
	var got ops.Changes
	if out == "" {
		return got, err
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jerr, out)
	}
	return got, err
}

func TestChangesFollowsTheServerCursorAcrossPages(t *testing.T) {
	f := feedFixture(450)
	got, err := changes(t, f, "-since", feedStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Todos) != 450 || got.Pages != 3 || !got.Complete || got.HasMore {
		t.Fatalf("todos=%d pages=%d complete=%v hasMore=%v, want 450 in 3 complete pages", len(got.Todos), got.Pages, got.Complete, got.HasMore)
	}
	for i, td := range got.Todos {
		if want := fmt.Sprintf("c%04d", i+1); td.ID != want {
			t.Fatalf("todo %d is %s, want %s: the feed order must be kept", i, td.ID, want)
		}
	}
	// Each pull starts at the cursor of the one before, not at a clock.
	want := []string{cur(0), cur(200), cur(400)}
	if !slices.Equal(f.FeedSince, want) {
		t.Errorf("pulled since %v, want %v", f.FeedSince, want)
	}
	if got.Cursor != cur(450) {
		t.Errorf("cursor=%v, want the server's %v", got.Cursor, cur(450))
	}
	if got.Since != cur(0) {
		t.Errorf("since = %v", got.Since)
	}
}

func TestChangesReportsTombstonesAndInlineChecklists(t *testing.T) {
	f := feedFixture(3)
	gone := feedAt(2)
	f.Todos[1].DeletedAt = &gone
	f.Steps["c0003"] = []api.ChecklistItem{
		{ID: "step-one", TodoID: "c0003", Title: "one"},
		{ID: "step-two", TodoID: "c0003", Title: "two", CompletedAt: &gone},
	}
	got, err := changes(t, f, "-since", feedStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Todos) != 3 {
		t.Fatalf("todos = %d", len(got.Todos))
	}
	if got.Todos[0].Deleted || !got.Todos[1].Deleted || got.Todos[1].DeletedAt == nil || got.Todos[2].Deleted {
		t.Errorf("deleted marks = %v %v %v, want only the second", got.Todos[0].Deleted, got.Todos[1].Deleted, got.Todos[2].Deleted)
	}
	if len(got.Todos[0].Checklist) != 0 {
		t.Errorf("c0001 has no steps: %+v", got.Todos[0].Checklist)
	}
	steps := got.Todos[2].Checklist
	if len(steps) != 2 || steps[0].ID != "step-one" || steps[1].ID != "step-two" || !steps[1].Completed() {
		t.Errorf("inline checklist = %+v", steps)
	}
	// The mark is in the JSON itself, for a reader that never decodes into our types.
	out, _ := run(t, f, "", "changes", "-json", "-since", feedStart)
	if !strings.Contains(out, `"deleted": true`) || !strings.Contains(out, `"deleted": false`) {
		t.Errorf("output does not mark tombstones:\n%s", out)
	}
}

func TestChangesEmptyResultKeepsEmptySnapshots(t *testing.T) {
	f := feedFixture(2)
	after := cur(10)
	out, err := run(t, f, "", "changes", "-json", "-since", after)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"todos": []`, `"invitations": []`, `"complete": true`, `"hasMore": false`, `"pages": 1`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `"cursor": "`+after+`"`) {
		t.Errorf("an empty result keeps the server's cursor:\n%s", out)
	}
	// A server that sends no snapshot is not saying there are none.
	f.OmitInvitations = true
	out, _ = run(t, f, "", "changes", "-json", "-since", after)
	if !strings.Contains(out, `"invitations": null`) {
		t.Errorf("a missing snapshot must not become an empty one:\n%s", out)
	}
}

func TestChangesSnapshotsAreTheLastPages(t *testing.T) {
	f := feedFixture(5)
	f.FeedPageSize = 2
	f.Invitations = []api.Invitation{{ID: "inv-old"}}
	f.OnFeed = func(n int) {
		if n == 3 {
			f.Lists = append(f.Lists, api.List{ID: "l9", Name: "Late"})
			f.Invitations = nil
		}
	}
	got, err := changes(t, f, "-since", feedStart)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pages != 3 || len(got.Todos) != 5 {
		t.Fatalf("pages=%d todos=%d", got.Pages, len(got.Todos))
	}
	if len(got.Lists) != 3 || got.Lists[2].ID != "l9" {
		t.Errorf("lists = %+v, want the snapshot from the last page", got.Lists)
	}
	if got.Invitations == nil || len(got.Invitations) != 0 {
		t.Errorf("invitations = %#v, want the last page's empty snapshot, not the first page's", got.Invitations)
	}
}

func TestChangesStopsAtThePageBoundAndSaysSo(t *testing.T) {
	f := feedFixture(450)
	f.FeedPageSize = 50
	out, err := run(t, f, "", "changes", "-json", "-since", feedStart, "-max-pages", "3")
	info := describe(err)
	if info.ExitCode != ops.ExitIncomplete || info.Code != "feed_incomplete" || !strings.Contains(info.Message, cur(150)) {
		t.Fatalf("err = %+v, want exit 10 naming the resume cursor", info)
	}
	var got ops.Changes
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("the partial result should still be printed: %v %q", jerr, out)
	}
	if got.Complete || !got.HasMore || got.Pages != 3 || len(got.Todos) != 150 || got.Cursor != cur(150) {
		t.Errorf("complete=%v hasMore=%v pages=%d todos=%d cursor=%v", got.Complete, got.HasMore, got.Pages, len(got.Todos), got.Cursor)
	}
	if f.FeedRequests != 3 {
		t.Errorf("pulled %d pages past a bound of 3", f.FeedRequests)
	}
	// Resuming from the cursor reads the rest, and together nothing is lost or repeated.
	rest, err := changes(t, f, "-since", got.Cursor, "-max-pages", "10")
	if err != nil || !rest.Complete || len(rest.Todos) != 300 || rest.Todos[0].ID != "c0151" {
		t.Fatalf("resume: err=%v complete=%v todos=%d", err, rest.Complete, len(rest.Todos))
	}
	// A bound that is exactly enough is complete, not incomplete.
	f.FeedPageSize = 150
	if got, err := changes(t, f, "-since", feedStart, "-max-pages", "3"); err != nil || !got.Complete || got.Pages != 3 {
		t.Errorf("exactly enough pages: err=%v complete=%v pages=%d", err, got.Complete, got.Pages)
	}
	// In text the summary says it too, and the exit code is the same.
	f.FeedPageSize = 50
	text, err := run(t, f, "", "changes", "-since", feedStart, "-max-pages", "2")
	if describe(err).ExitCode != ops.ExitIncomplete || !strings.Contains(text, "incomplete; resume with -since "+cur(100)) {
		t.Errorf("text = %q err = %v", text, err)
	}
}

func TestChangesChecksTheCursorBeforeAskingAnything(t *testing.T) {
	// The first three are what -since used to take: a time. The service
	// refuses them as cursors, so they are refused here with a reason.
	for _, since := range []string{"yesterday", "2026-03-10", "2026-03-10T15:00:00Z", "2026-03-10T15:00:00+01:00", "1234567890123456", "12 34"} {
		f := feedFixture(3)
		_, err := run(t, f, "", "changes", "-since", since)
		if describe(err).ExitCode != ops.ExitUsage || !strings.Contains(errorText(err), "-since") {
			t.Errorf("-since %q: %v, want a usage error naming -since", since, err)
		}
		if f.FeedRequests != 0 {
			t.Errorf("-since %q made %d requests", since, f.FeedRequests)
		}
	}
	f := feedFixture(3)
	for _, more := range [][]string{{"-max-pages", "0"}, {"-max-pages", "-2"}, {"-max-pages", "lots"}, {"extra"}} {
		args := append([]string{"changes", "-since", feedStart}, more...)
		if _, err := run(t, f, "", args...); describe(err).ExitCode != ops.ExitUsage || f.FeedRequests != 0 {
			t.Errorf("%v: %v, want a usage error and no request", more, err)
		}
	}
}

func TestChangesWithoutACursorReadsEverythingAndHandsOneOut(t *testing.T) {
	f := feedFixture(450)
	got, err := changes(t, f)
	if err != nil {
		t.Fatal(err)
	}
	// The first pull is an initial sync, which sends no cursor; the rest
	// follow the ones the service gave.
	if want := []string{"", cur(200), cur(400)}; !slices.Equal(f.FeedSince, want) {
		t.Errorf("pulled from %q, want %q", f.FeedSince, want)
	}
	if len(got.Todos) != 450 || !got.Complete || got.Since != "" || got.Cursor != cur(450) {
		t.Errorf("todos=%d complete=%v since=%q cursor=%q", len(got.Todos), got.Complete, got.Since, got.Cursor)
	}
}

func TestChangesRefusesListingFilters(t *testing.T) {
	for _, flag := range [][]string{{"-status", "open"}, {"-list", "Work"}, {"-q", "milk"}, {"-assigned", "me"}, {"-limit", "5"}} {
		f := feedFixture(3)
		args := append([]string{"changes", "-since", feedStart}, flag...)
		out, err := run(t, f, "", args...)
		if describe(err).ExitCode != ops.ExitUsage || !strings.Contains(errorText(err), flag[0]) {
			t.Errorf("%s: %v, want a usage error naming it", flag[0], err)
		}
		if f.FeedRequests != 0 || out != "" {
			t.Errorf("%s: requests=%d output=%q, want nothing done", flag[0], f.FeedRequests, out)
		}
	}
}

func TestChangesFailureOnALaterPageClaimsNothing(t *testing.T) {
	cases := map[string]struct {
		err  *api.Error
		exit int
	}{
		"server error": {&api.Error{StatusCode: 503, Code: api.CodeUpstreamFailed}, ops.ExitServer},
		"rate limit":   {&api.Error{StatusCode: 429, Code: api.CodeRateLimited}, ops.ExitRateLimited},
		"rejected":     {&api.Error{StatusCode: 401, Code: api.CodeUnauthorized}, ops.ExitAuth},
	}
	for name, tc := range cases {
		f := feedFixture(450)
		f.FeedError = func(n int) *api.Error {
			if n == 2 {
				return tc.err
			}
			return nil
		}
		for _, args := range [][]string{{"-json"}, {}} {
			f.FeedRequests = 0
			out, err := run(t, f, "", append([]string{"changes", "-since", feedStart}, args...)...)
			if describe(err).ExitCode != tc.exit {
				t.Errorf("%s: exit %d, want %d (%v)", name, describe(err).ExitCode, tc.exit, err)
			}
			if out != "" || strings.Contains(out, "complete") {
				t.Errorf("%s: a failed walk printed %q", name, out)
			}
		}
	}
}

func TestChangesTextSummary(t *testing.T) {
	f := feedFixture(3)
	gone := feedAt(1)
	f.Todos[0].DeletedAt = &gone
	out, err := run(t, f, "", "changes", "-since", feedStart)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 changes (1 deleted) in 1 pages", "lists 2, invitations 0", "complete; next -since " + cur(3)} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

// crowd adds n todos whose ids are t0000x, t0001x and so on, so that t0001
// is a short id for exactly one of them.
func crowd(f *testapi.API, n int) {
	for i := 0; i < n; i++ {
		f.Todos = append(f.Todos, api.Todo{ID: fmt.Sprintf("t%04dx", i), ListID: "l1", Title: fmt.Sprintf("Task %d", i)})
	}
}

func TestBatchOfShortIdsSharesOneListing(t *testing.T) {
	for _, c := range []struct{ one, three []string }{
		{[]string{"done", "t0001"}, []string{"done", "t0001", "t0500", "t0999"}},
		{[]string{"rm", "-yes", "t0001"}, []string{"rm", "-yes", "t0001", "t0500", "t0999"}},
	} {
		// What one short id costs is one listing of the account, which is
		// five pages of the change feed here.
		one := testapi.New()
		crowd(one, 1000)
		if _, err := run(t, one, "", c.one...); err != nil {
			t.Fatal(err)
		}
		if one.FeedRequests < 5 {
			t.Fatalf("%s with one id pulled %d pages, want a listing of 5", c.one[0], one.FeedRequests)
		}

		f := testapi.New()
		crowd(f, 1000)
		out, err := run(t, f, "", c.three...)
		if err != nil {
			t.Fatal(err)
		}
		if f.FeedRequests != one.FeedRequests {
			t.Errorf("%s with three short ids pulled %d pages, want the %d one listing costs", c.three[0], f.FeedRequests, one.FeedRequests)
		}
		// The results come back in the order the ids were given.
		if a, b, d := strings.Index(out, "t0001x"), strings.Index(out, "t0500x"), strings.Index(out, "t0999x"); a < 0 || a > b || b > d {
			t.Errorf("%s: output is out of order: %q", c.three[0], out)
		}
	}
}

func TestBatchReadsEarlierChangesInLaterReferences(t *testing.T) {
	f := testapi.New()
	crowd(f, 10)
	out, err := run(t, f, "", "done", "t0001", "t0002", "t0001")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 ||
		!strings.HasPrefix(lines[0], "done t0001x") || !strings.HasPrefix(lines[1], "done t0002x") || !strings.HasPrefix(lines[2], "already done t0001x") {
		t.Errorf("a repeated id should be reported done already: %q", out)
	}

	f = testapi.New()
	crowd(f, 10)
	out, err = run(t, f, "", "rm", "-yes", "t0001", "t0001")
	if describe(err).ExitCode != ops.ExitNotFound {
		t.Errorf("deleting the same short id twice: err = %v, want the second to be not found", err)
	}
	if strings.Count(out, "deleted") != 1 || f.Find("t0001x") != nil {
		t.Errorf("out = %q", out)
	}
}

func TestBatchStillPrefersAnExactId(t *testing.T) {
	f := testapi.New()
	f.Todos = append(f.Todos,
		api.Todo{ID: "abc", ListID: "l1", Title: "Exact"},
		api.Todo{ID: "abcd", ListID: "l1", Title: "Longer"})
	out, err := run(t, f, "", "done", "abc", "abcd")
	if err != nil || !strings.Contains(out, "done abc  Exact") || !strings.Contains(out, "done abcd  Longer") {
		t.Errorf("out = %q err = %v", out, err)
	}
	if !f.Find("abc").IsCompleted || !f.Find("abcd").IsCompleted {
		t.Error("both should be done")
	}
}

// Each date flag reads a bare day in words the way it reads the same day
// written as a date, and a day with a time as that time, on both paths. The
// server is the stand-in's canned answer for those words, which is what the
// service gives: a do date at midnight and no time for "friday", and the
// time for "friday 5pm". The local reader runs for real against today's
// date, so only what does not depend on today is checked.
func TestDateFlagsReadAWeekdayAlone(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "Europe/Madrid")
	friday := time.Date(2026, 3, 13, 0, 0, 0, 0, madrid)
	fivePM := friday.Add(17 * time.Hour)
	answers := map[string]api.ParsePreview{
		"friday":     {DoAt: &friday},
		"friday 5pm": {DoAt: &fivePM, HasTime: true},
	}
	check := func(t *testing.T, flag, words string, got api.Todo) {
		t.Helper()
		var at *time.Time
		switch flag {
		case "day":
			day, err := time.ParseInLocation("2006-01-02", deref(got.ScheduledDay), madrid)
			if err != nil || day.Weekday() != time.Friday {
				t.Errorf("day = %q (%v), want a Friday", deref(got.ScheduledDay), err)
			}
			return
		case "at":
			at = got.DoAt
		case "due":
			at = got.DueAt
		case "remind":
			at = got.RemindAt
		}
		if at == nil {
			t.Fatal("the date was not saved")
		}
		when := at.In(madrid)
		if when.Weekday() != time.Friday {
			t.Errorf("saved %v, want a Friday", when)
		}
		if words == "friday 5pm" {
			if when.Hour() != 17 || when.Minute() != 0 || (flag == "at" && !got.HasTime) {
				t.Errorf("saved %v with hasTime %v, want 17:00 and a time", when, got.HasTime)
			}
			return
		}
		if flag == "at" && got.HasTime {
			t.Error("a do date from a day alone has hasTime true")
		}
		if flag != "at" && (when.Hour() != 0 || when.Minute() != 0) {
			t.Errorf("saved %v, want midnight, the day with no time of day", when)
		}
	}
	change := func(t *testing.T, out string) api.Todo {
		t.Helper()
		var ch ops.Change
		if err := json.Unmarshal([]byte(out), &ch); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return ch.Todo
	}
	for _, flag := range []string{"day", "at", "due", "remind"} {
		for _, words := range []string{"friday", "friday 5pm"} {
			t.Run("server "+flag+" "+words, func(t *testing.T) {
				f := testapi.New()
				f.Parsed = answers[words]
				out, err := run(t, f, "", "edit", "aaa222", "-json", "-"+flag, words)
				if err != nil {
					t.Fatal(err)
				}
				check(t, flag, words, change(t, out))
			})
			t.Run("local "+flag+" "+words, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "todos.json")
				out, err := runLocal(t, path, "add", "-json", "Water the plants")
				if err != nil {
					t.Fatal(err)
				}
				var added struct{ Todos []api.Todo }
				if err := json.Unmarshal([]byte(out), &added); err != nil || len(added.Todos) != 1 {
					t.Fatalf("add -json: %s %v", out, err)
				}
				out, err = runLocal(t, path, "edit", added.Todos[0].ID, "-json", "-"+flag, words)
				if err != nil {
					t.Fatal(err)
				}
				check(t, flag, words, change(t, out))
			})
		}
	}
}
