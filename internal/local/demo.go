package local

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	"github.com/nerdlabz/evado-tui/internal/api"
)

//go:embed demo_titles.txt
var demoTitles string

// SeedDemo fills a throwaway, unsigned store. Recording scripts read the
// same titles, whose relative dates keep every view useful on any day.
func (b *Backend) SeedDemo(ctx context.Context) error {
	b.mu.Lock()
	signed := b.remote != nil || b.signedIn()
	yesterday := b.now().In(b.loc()).AddDate(0, 0, -1).Format("2006-01-02")
	b.mu.Unlock()
	if signed {
		return errors.New("demo samples need a store with no account")
	}
	c := b.Client()
	// Capture deliberately leaves phrases about the past alone. An ISO
	// date gives this sample a definite overdue day without guessing.
	//
	// Each line is a capture of its own. A capture of several lines keeps
	// all of them as what was typed, on every todo it makes, and a sample
	// todo should show only its own line.
	var made []api.Todo
	for _, line := range strings.Split(strings.ReplaceAll(demoTitles, "{{yesterday}}", yesterday), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		todos, err := c.CreateTodos(ctx, api.NewTodo{Title: line})
		if err != nil {
			return err
		}
		made = append(made, todos...)
	}
	for _, todo := range made {
		switch {
		case strings.HasPrefix(todo.Title, "Buy oat milk"), strings.HasPrefix(todo.Title, "Book the flights"):
			if _, _, err := c.CompleteTodo(ctx, todo.ID); err != nil {
				return err
			}
		case strings.HasPrefix(todo.Title, "Renew the passport"):
			for _, step := range []string{"Get new photos taken", "Fill in the form", "Book the appointment"} {
				if _, err := c.AddChecklistItem(ctx, todo.ID, step); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
