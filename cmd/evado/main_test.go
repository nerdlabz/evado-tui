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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestMainProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(),
		"EVADO_TEST_MAIN=1", "EVADO_CONFIG="+cfgPath,
		"EVADO_TOKEN=", "EVADO_SERVER=", "EVADO_LOCAL=", "EVADO_JSON=")
	cmd.Env = append(cmd.Env, env...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("evado %v: %v", args, err)
	}
	return errOut.String(), cmd.ProcessState.ExitCode()
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
