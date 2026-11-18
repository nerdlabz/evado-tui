package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
	"github.com/nerdlabz/evado-tui/internal/ops"
)

func TestLocalMode(t *testing.T) {
	token := config.Config{Token: "evt_x"}
	noAccount := config.Config{Local: true}
	for _, c := range []struct {
		name          string
		flagSet, flag bool
		env           string
		cfg           config.Config
		want          bool
	}{
		{"a token goes to the server", false, false, "", token, false},
		{"no token and no choice is the usual error", false, false, "", config.Config{}, false},
		{"no token on a computer set up without an account", false, false, "", noAccount, true},
		{"but not when it keeps no todos", false, false, "", config.Config{Local: true, NoCache: true}, false},
		{"-local beats a token", true, true, "", token, true},
		{"-local=false beats the setup", true, false, "", noAccount, false},
		{"-local=false beats the environment", true, false, "1", config.Config{}, false},
		{"EVADO_LOCAL beats a token", false, false, "1", token, true},
		{"EVADO_LOCAL=true beats a token", false, false, "true", token, true},
		// Anything but a yes leaves it to the rows below, as if it were unset.
		{"EVADO_LOCAL=0 with a token is the server", false, false, "0", token, false},
		{"EVADO_LOCAL=0 with no token and no choice is the usual error", false, false, "0", config.Config{}, false},
		{"EVADO_LOCAL=0 keeps the setup without an account", false, false, "0", noAccount, true},
		{"EVADO_LOCAL=false keeps the setup without an account", false, false, "false", noAccount, true},
		{"EVADO_LOCAL that is not a yes keeps the setup", false, false, "maybe", noAccount, true},
		{"EVADO_LOCAL=0 does not use a setup that keeps no todos", false, false, "0", config.Config{Local: true, NoCache: true}, false},
	} {
		if got := localMode(c.flagSet, c.flag, c.env, c.cfg); got != c.want {
			t.Errorf("%s: localMode = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestServerOnlyCommandsAreRefusedOnThisComputer(t *testing.T) {
	serverOnlyCmds := []string{"changes", "claim", "release"}
	for _, cmd := range serverOnlyCmds {
		err := serverOnly([]string{cmd, "3fa9"})
		if info := describe(err); info.ExitCode != ops.ExitUsage || info.Code != "needs_server" || !strings.Contains(info.Message, cmd) {
			t.Errorf("%s: %+v, want exit 2, needs_server and the command named", cmd, info)
		}
	}
	for _, cmd := range commandNames {
		if !slices.Contains(serverOnlyCmds, cmd) && serverOnly([]string{cmd}) != nil {
			t.Errorf("%s is refused on this computer, but works there", cmd)
		}
	}
}

// A command only the service can answer is refused before the todos here
// are opened, so a damaged or busy file cannot hide why it will never work.
func TestServerOnlyCommandsAreRefusedBeforeTheFileIsRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EVADO_CONFIG", filepath.Join(dir, "config.json"))
	todos := filepath.Join(dir, "todos.json")
	if err := os.WriteFile(todos, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"changes", "claim", "release"} {
		_, err := commandClient(config.Config{}, []string{cmd, "3fa9"}, true)
		if info := describe(err); info.ExitCode != ops.ExitUsage || info.Code != "needs_server" {
			t.Errorf("%s: %+v, want exit 2 and needs_server", cmd, info)
		}
	}
	if _, err := os.Stat(todos + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the lock file was taken: %v", err)
	}
	// The same file stops a command that does work here.
	if _, err := commandClient(config.Config{}, []string{"ls"}, true); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("ls on a damaged file: %v, want it reported", err)
	}
}

// runLocal runs a command against the todos kept at path, as -local does.
func runLocal(t *testing.T, path string, args ...string) (string, error) {
	t.Helper()
	b, err := local.Open(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = runCommand(context.Background(), ops.New(b.Client()), args, &out, nil, wantsJSON(args, ""))
	return out.String(), err
}

// On this computer, parse says when it had to guess the half of the day,
// in words and in -json, as it does against the service.
func TestParseOnThisComputerSaysWhenATimeWasAssumed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	out, err := runLocal(t, path, "parse", "wake up tomorrow at 8")
	if err != nil || !strings.Contains(out, `assumed "at 8" was read as AM; type 8pm for evening`) {
		t.Errorf("parse: %q %v", out, err)
	}
	out, err = runLocal(t, path, "parse", "-json", "wake up tomorrow at 8")
	var got struct {
		Parse struct {
			AssumedTime *struct{ Text, Assumed, AlternativeText string }
		}
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Parse.AssumedTime == nil ||
		got.Parse.AssumedTime.Text != "at 8" || got.Parse.AssumedTime.Assumed != "am" || got.Parse.AssumedTime.AlternativeText != "at 8pm" {
		t.Errorf("parse -json: %q %v", out, err)
	}
	out, err = runLocal(t, path, "parse", "wake up tomorrow at 8am")
	if err != nil || strings.Contains(out, "assumed") {
		t.Errorf("parse of a time that says which half: %q %v", out, err)
	}
}

// Every command that makes sense without the service works on the file,
// short ids included, which are matched by reading every todo.
func TestCommandsWorkOnTheTodosHere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	out, err := runLocal(t, path, "add", "-json", "Call the bank 2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	var added struct {
		Todos []struct {
			ID           string `json:"id"`
			ScheduledDay string `json:"scheduledDay"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || len(added.Todos) != 1 {
		t.Fatalf("add -json: %s %v", out, err)
	}
	id := added.Todos[0].ID
	if added.Todos[0].ScheduledDay != "2026-03-10" {
		t.Errorf("day = %q, want the date read from the text", added.Todos[0].ScheduledDay)
	}
	short := id[:6]
	for _, args := range [][]string{
		{"ls", "-status", "all"},
		{"show", short},
		{"edit", short, "-notes", "ask about fees", "-due", "none"},
		{"step", short, "Find the card"},
		{"steps", "list", short},
		{"done", short},
		{"undone", short},
		{"lists"},
		{"parse", "lunch friday 1pm"},
	} {
		if _, err := runLocal(t, path, args...); err != nil {
			t.Errorf("%s: %v", strings.Join(args, " "), err)
		}
	}
	out, err = runLocal(t, path, "show", short)
	if err != nil || !strings.Contains(out, "ask about fees") || !strings.Contains(out, "Find the card") {
		t.Errorf("show after the edits: %q %v", out, err)
	}
	if _, err := runLocal(t, path, "rm", short); ops.ExitCode(err) != ops.ExitUsage {
		t.Errorf("rm without -yes: %v, want exit 2", err)
	}
	if _, err := runLocal(t, path, "rm", "-yes", short); err != nil {
		t.Fatal(err)
	}
	if _, err := runLocal(t, path, "show", short); ops.ExitCode(err) != ops.ExitNotFound {
		t.Errorf("show after rm: %v, want exit 4", err)
	}
}

// A time that has passed today is reported by parse, either reading can be
// asked for with -passed, and add keeps the choice. "today at 12am" has
// always passed, whatever the clock says when the test runs.
func TestPassedTimesOnThisComputer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todos.json")
	const text = "Call Steven today at 12am"

	out, err := runLocal(t, path, "parse", text)
	if err != nil || !strings.Contains(out, "passed  that time has passed, so it stays on today with no reminder") {
		t.Errorf("parse: %q, %v; want the kept reading said", out, err)
	}
	out, err = runLocal(t, path, "parse", "-passed", "moved", text)
	if err != nil || !strings.Contains(out, "passed  that time has passed today, so it is for tomorrow") {
		t.Errorf("parse -passed moved: %q, %v; want the moved reading said", out, err)
	}
	var parsed struct {
		Parse struct {
			TimePassed *string `json:"timePassed"`
		} `json:"parse"`
	}
	out, err = runLocal(t, path, "parse", "-json", text)
	if jerr := json.Unmarshal([]byte(out), &parsed); err != nil || jerr != nil || parsed.Parse.TimePassed == nil || *parsed.Parse.TimePassed != "kept" {
		t.Errorf("parse -json: %q, %v; want timePassed kept", out, err)
	}
	out, err = runLocal(t, path, "parse", "-json", "Call Steven tomorrow at 12am")
	if !strings.Contains(out, `"timePassed": null`) {
		t.Errorf("parse -json with no passed time: %q, %v; want timePassed null", out, err)
	}

	var added struct {
		Todos []struct {
			DoAt     *string `json:"doAt"`
			RemindAt *string `json:"remindAt"`
		} `json:"todos"`
	}
	out, err = runLocal(t, path, "add", "-json", text)
	if jerr := json.Unmarshal([]byte(out), &added); err != nil || jerr != nil || len(added.Todos) != 1 || added.Todos[0].DoAt == nil || added.Todos[0].RemindAt != nil {
		t.Errorf("add: %q, %v; want a do date and no reminder", out, err)
	}
	out, err = runLocal(t, path, "add", "-json", "-passed", "moved", text)
	if jerr := json.Unmarshal([]byte(out), &added); err != nil || jerr != nil || len(added.Todos) != 1 || added.Todos[0].RemindAt == nil {
		t.Errorf("add -passed moved: %q, %v; want a reminder", out, err)
	}

	for _, cmd := range []string{"add", "parse"} {
		if _, err := runLocal(t, path, cmd, "-passed", "later", text); describe(err).ExitCode != ops.ExitUsage {
			t.Errorf("%s -passed later: %v; want a usage error", cmd, err)
		}
	}
}
