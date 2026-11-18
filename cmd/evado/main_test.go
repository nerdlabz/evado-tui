package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/ops"
)

// TestMainProcess is not a test: it is the evado binary, for runMain.
func TestMainProcess(t *testing.T) {
	if os.Getenv("EVADO_TEST_MAIN") != "1" {
		t.Skip("run by runMain")
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	os.Args = append([]string{"evado"}, args...)
	flag.CommandLine = flag.NewFlagSet("evado", flag.ExitOnError)
	main()
	os.Exit(0)
}

// runMain runs evado with args in a process of its own, since main exits,
// with the config at cfgPath and nothing from the shell running the tests.
func runMain(t *testing.T, cfgPath string, env []string, args ...string) (stderr string, code int) {
	t.Helper()
	_, stderr, code = runMainOut(t, cfgPath, env, args...)
	return stderr, code
}

// A config that cannot be read is reported with its path and left as it
// was, by commands, -save, -check and the TUI alike, instead of being taken
// for no config and later written over.
func TestADamagedConfigIsReportedAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	damaged := []byte(`{"token": "evt_saved", "theme": `)
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		env  []string
		args []string
	}{
		{"a command", nil, []string{"ls"}},
		{"a command with a token from the environment", []string{"EVADO_TOKEN=evt_env", "EVADO_SERVER=http://127.0.0.1:1"}, []string{"ls"}},
		{"-local", nil, []string{"-local", "ls"}},
		{"-save", nil, []string{"-save", "-token", "evt_new"}},
		{"-check", nil, []string{"-check"}},
		{"the TUI", nil, nil},
	} {
		stderr, code := runMain(t, path, c.env, c.args...)
		if code != ops.ExitFailure || !strings.Contains(stderr, path) || !strings.Contains(stderr, "move it aside") {
			t.Errorf("%s: exit %d, %q; want exit 1 naming %s", c.name, code, stderr, path)
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, damaged) {
			t.Fatalf("%s: the config was changed to %q", c.name, got)
		}
	}

	// With -json the error is the usual JSON one.
	stderr, code := runMain(t, path, nil, "ls", "-json")
	var out struct {
		Error ops.Info `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &out); err != nil || code != ops.ExitFailure || out.Error.ExitCode != ops.ExitFailure || !strings.Contains(out.Error.Message, path) {
		t.Errorf("-json: exit %d, %q (%v); want a JSON error with exit 1 naming the file", code, stderr, err)
	}

	// No config at all is a first run, not an error.
	stderr, code = runMain(t, filepath.Join(t.TempDir(), "config.json"), nil, "ls")
	if code != ops.ExitAuth || !strings.Contains(stderr, "no token") {
		t.Errorf("no config: exit %d, %q; want the no-token error", code, stderr)
	}
}

// runMainOut is runMain that also returns standard output.
func runMainOut(t *testing.T, cfgPath string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestMainProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(),
		"EVADO_TEST_MAIN=1", "EVADO_CONFIG="+cfgPath,
		"EVADO_TOKEN=", "EVADO_SERVER=", "EVADO_LOCAL=", "EVADO_JSON=")
	cmd.Env = append(cmd.Env, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("evado %v: %v", args, err)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

// Help is printed on standard output with exit 0, and needs no token, no
// todos on this computer and no readable config.
func TestHelpNeedsNoAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"help"}, "evado add [-list name]"},
		{[]string{"help", "add"}, "usage: evado " + addSynopsis},
		{[]string{"add", "-h"}, "usage: evado " + addSynopsis},
		{[]string{"add", "--help"}, "-notes string"},
		{[]string{"edit", "3fa9", "-help"}, "usage: evado " + editSynopsis},
		{[]string{"steps", "-h"}, "evado steps done|undone"},
		{[]string{"help", "steps"}, "evado steps done|undone"},
		{[]string{"steps", "rm", "-h"}, "usage: evado steps rm"},
		{[]string{"-local", "ls", "-h"}, "-limit"},
	} {
		stdout, stderr, code := runMainOut(t, path, nil, c.args...)
		if code != 0 || !strings.Contains(stdout, c.want) || stderr != "" {
			t.Errorf("evado %v: exit %d, stdout %q, stderr %q; want %q on stdout and exit 0", c.args, code, stdout, stderr, c.want)
		}
	}

	if err := os.WriteFile(path, []byte(`{"token": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if stdout, _, code := runMainOut(t, path, nil, "ls", "-h"); code != 0 || !strings.Contains(stdout, "usage: evado ls") {
		t.Errorf("with a damaged config: exit %d, %q; want the usage", code, stdout)
	}
}

// Every command answers -h with its own usage.
func TestEveryCommandHasHelp(t *testing.T) {
	for _, name := range commandNames {
		if text, ok := commandHelp([]string{name, "-h"}); !ok || !strings.HasPrefix(text, "usage: evado ") || !strings.Contains(text, "-json") {
			t.Errorf("evado %s -h printed %q; want its usage and flags", name, text)
		}
	}
}

// A -h that is another flag's value, or comes after --, is text for the
// todo and not a request for help.
func TestHelpIsNotReadFromAFlagsValue(t *testing.T) {
	for _, args := range [][]string{
		{"add", "-notes", "-h", "Buy milk"},
		{"add", "--", "-h"},
		{"add", "Buy milk"},
	} {
		if text, ok := commandHelp(args); ok {
			t.Errorf("commandHelp(%v) = %q; want no help", args, text)
		}
	}
}

// A word that is not a command is a usage error, exit 2, printed as JSON
// when the environment asks for that.
func TestAnUnknownCommandIsAUsageError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, args := range [][]string{{"frobnicate"}, {"help", "frobnicate"}} {
		_, stderr, code := runMainOut(t, path, nil, args...)
		if code != ops.ExitUsage || !strings.Contains(stderr, `unknown command "frobnicate"`) || !strings.Contains(stderr, "evado help") {
			t.Errorf("evado %v: exit %d, %q; want exit 2 naming the command", args, code, stderr)
		}
	}
	_, stderr, code := runMainOut(t, path, []string{"EVADO_JSON=1"}, "frobnicate")
	var out struct {
		Error ops.Info `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &out); err != nil || code != ops.ExitUsage || out.Error.Code != "usage" || out.Error.ExitCode != ops.ExitUsage {
		t.Errorf("EVADO_JSON=1: exit %d, %q (%v); want a JSON usage error", code, stderr, err)
	}
}
