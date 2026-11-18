package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
)

func TestDemoUsesTemporarySamplesAndLeavesTheRealProfileAlone(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "config.json")
	t.Setenv("EVADO_CONFIG", real)
	t.Setenv("EVADO_TOKEN", "evt_real")
	t.Setenv("EVADO_SERVER", "https://account.example/api/v1")
	for _, name := range []string{"config.json", "todos.json", "session.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, cleanup, err := prepareDemo()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	path, _ := config.Path()
	if path == real || !cfg.Demo || !cfg.Local || cfg.Token != "" || cfg.BaseURL != "" {
		t.Fatalf("demo profile = %+v at %s", cfg, path)
	}
	b, err := local.Open(filepath.Join(filepath.Dir(path), "todos.json"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status().SignedIn {
		t.Fatal("demo has an account")
	}
	todos, err := b.Client().ListTodos(context.Background(), api.TodoFilter{Status: api.StatusAll})
	if err != nil || len(todos) != 8 {
		t.Fatalf("samples = %d, %v", len(todos), err)
	}
	done, overdue, timed := 0, 0, 0
	for _, todo := range todos {
		// Each sample is a capture of its own, so what was typed is one line.
		if todo.RawInput == nil || strings.Contains(*todo.RawInput, "\n") || !strings.HasPrefix(*todo.RawInput, todo.Title) {
			t.Errorf("%q was typed as %v; want its own line", todo.Title, todo.RawInput)
		}
		if todo.IsCompleted {
			done++
		}
		if todo.HasTime {
			timed++
		}
		if todo.DoAt != nil && todo.DoAt.Before(time.Now()) {
			overdue++
		}
	}
	if done != 2 || overdue == 0 || timed < 2 {
		t.Fatalf("samples have %d completed, %d overdue and %d timed", done, overdue, timed)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("temporary demo directory remains")
	}
	if restored, _ := config.Path(); restored != real {
		t.Fatal("profile environment was not restored")
	}
	for _, name := range []string{"config.json", "todos.json", "session.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "keep "+name {
			t.Fatalf("real %s changed", name)
		}
	}
}
