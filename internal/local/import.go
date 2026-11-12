package local

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
)

// Import adds an export without replacing existing todos, including tombstones.
// Signed-in stores send the new todos through their ordinary offline sync.
func (b *Backend) Import(ex api.Export, options ...api.ImportOptions) (api.ImportResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.diskErr != nil {
		return api.ImportResult{}, b.diskErr
	}
	if len(ex.Todos) > api.MaxImportTodos {
		return api.ImportResult{}, &api.Error{StatusCode: http.StatusBadRequest, Code: "invalid_export", Message: "an export can contain at most 25000 todos"}
	}
	opts := api.ImportOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.ListID != "" {
		l := b.list(opts.ListID)
		if l == nil {
			return api.ImportResult{}, &api.Error{StatusCode: http.StatusNotFound, Code: "not_found", Message: "no such list"}
		}
		if l.ReadOnly {
			return api.ImportResult{}, &api.Error{StatusCode: http.StatusForbidden, Code: "list_read_only", Message: "that list is read-only"}
		}
	}
	before, next := b.st, b.st
	next.Lists, next.Todos = slices.Clone(next.Lists), slices.Clone(next.Todos)
	next.ImportedIDs = maps.Clone(next.ImportedIDs)
	if next.ImportedIDs == nil {
		next.ImportedIDs = map[string]string{}
	}
	res := api.ImportResult{DryRun: opts.DryRun, Format: "evado", Complete: true, Refused: []api.ImportRefusal{}, Lists: api.ImportLists{Created: []string{}, Used: []string{}}, Dropped: map[string]int{}}
	refuse := func(kind, id, why string, item int) {
		res.Refused = append(res.Refused, api.ImportRefusal{Kind: kind, ID: id, Reason: why, Item: item})
		res.RefusedCount++
	}
	use := func(name string) {
		if !slices.Contains(res.Lists.Used, name) {
			res.Lists.Used = append(res.Lists.Used, name)
		}
	}
	listMap := map[string]string{"": b.personal()}
	seenLists := map[string]bool{}
	for _, in := range ex.Lists {
		if opts.ListID != "" {
			listMap[in.ID] = opts.ListID
			continue
		}
		if in.ID == "" || seenLists[in.ID] {
			refuse("list", in.ID, "missing or duplicate id", 0)
			continue
		}
		seenLists[in.ID] = true
		if in.IsPersonal {
			listMap[in.ID] = b.personal()
			res.ListsSkipped++
			continue
		}
		name, problem := listName(in.Name)
		if problem != nil {
			refuse("list", in.ID, problem.msg, 0)
			continue
		}
		if existing := b.list(in.ID); existing != nil {
			if existing.ReadOnly {
				refuse("list", in.ID, "its list is read-only", 0)
				continue
			}
			listMap[in.ID] = existing.ID
			res.ListsSkipped++
			continue
		}
		if slices.ContainsFunc(b.st.Deleted, func(l List) bool { return l.ID == in.ID }) {
			refuse("list", in.ID, "deleted here; not restored by import", 0)
			continue
		}
		matched := slices.IndexFunc(next.Lists, func(l List) bool {
			return l.Name == name && l.OwnerID == next.User.ID && !l.ReadOnly && !l.IsPersonal && !slices.ContainsFunc(l.Members, func(m api.Member) bool { return m.UserID != next.User.ID })
		})
		if matched >= 0 {
			listMap[in.ID] = next.Lists[matched].ID
			res.ListsSkipped++
			continue
		}
		l := List{List: api.List{ID: in.ID, Name: name, OwnerID: next.User.ID, Role: "owner", CreatedAt: b.now().UTC()}, Local: true, Members: []api.Member{{UserID: next.User.ID, Email: next.User.Email, Username: next.User.Username, Role: "owner"}}}
		next.Lists = append(next.Lists, l)
		listMap[in.ID] = l.ID
		res.ListsAdded++
		res.Lists.Created = append(res.Lists.Created, name)
	}
	seenTodos, seenSteps := map[string]bool{}, map[string]bool{}
	deletedTodos := map[string]bool{}
	for _, todo := range b.st.Todos {
		seenTodos[todo.ID] = true
		deletedTodos[todo.ID] = todo.DeletedAt != nil
		for _, step := range todo.Steps {
			seenSteps[step.ID] = true
		}
	}
	for item, in := range ex.Todos {
		if id, imported := next.ImportedIDs[in.ID]; in.ID != "" && imported {
			res.TodosSkipped++
			res.Skipped++
			if deletedTodos[id] || !seenTodos[id] {
				res.SkippedDeleted++
			}
			continue
		}
		// Check identity before validation so even malformed copies cannot restore a deletion.
		if in.ID != "" && seenTodos[in.ID] {
			res.TodosSkipped++
			res.Skipped++
			if deletedTodos[in.ID] {
				res.SkippedDeleted++
			}
			continue
		}
		if in.ID == "" {
			refuse("todo", in.ID, "missing id", item+1)
			continue
		}
		seenTodos[in.ID] = true
		listID := listMap[in.ListID]
		if opts.ListID != "" {
			listID = opts.ListID
		}
		if listID == "" {
			refuse("todo", in.ID, "its list was not imported", item+1)
			continue
		}
		if existing := b.list(listID); existing != nil && existing.ReadOnly {
			refuse("todo", in.ID, "its list is read-only", item+1)
			continue
		}
		if why := importTodoProblem(in); why != "" {
			refuse("todo", in.ID, why, item+1)
			continue
		}
		in.ListID = listID
		if in.AssigneeID != nil {
			res.Dropped["assignees"]++
			in.AssigneeID = nil
		}
		in.RemindedAt = nil
		if in.RemindAt != nil && (in.IsCompleted || !in.RemindAt.After(b.now())) {
			in.RemindAt = nil
		}
		steps := slices.Clone(in.Checklist)
		for i := range steps {
			if steps[i].ID == "" || seenSteps[steps[i].ID] {
				steps[i].ID = NewID()
			}
			seenSteps[steps[i].ID] = true
			steps[i].TodoID = in.ID
		}
		next.Todos = append(next.Todos, Todo{Todo: in.Todo, Steps: steps, New: true, ImportedID: in.ID, Spawned: in.IsCompleted && in.RecurrenceRule != nil})
		next.ImportedIDs[in.ID] = in.ID
		res.TodosAdded++
		res.Added++
		for _, l := range next.Lists {
			if l.ID == listID {
				use(l.Name)
				break
			}
		}
	}
	if opts.DryRun || res.ListsAdded+res.TodosAdded == 0 {
		return res, nil
	}
	b.st = next
	if err := b.persist(); err != nil {
		b.st = before
		return api.ImportResult{}, err
	}
	return res, nil
}

func importTodoProblem(in api.SyncTodo) string {
	switch {
	case in.DeletedAt != nil:
		return "exports must contain live todos"
	case strings.TrimSpace(in.Title) == "" || TextUnits(in.Title) > 500:
		return "title must contain 1 to 500 characters"
	case in.Notes != nil && TextUnits(*in.Notes) > 10000:
		return "notes are at most 10000 characters"
	case in.RecurrenceRule != nil && !ValidRule(*in.RecurrenceRule):
		return "repeat rule is not supported"
	case in.CreatedAt.IsZero() || in.UpdatedAt.IsZero():
		return "created and updated dates are required"
	case in.HasTime && in.DoAt == nil:
		return "a timed todo needs its do date"
	}
	if in.ScheduledDay != nil {
		if _, err := time.Parse("2006-01-02", *in.ScheduledDay); err != nil {
			return "scheduled day is not an ISO date"
		}
	}
	for _, step := range in.Checklist {
		if strings.TrimSpace(step.Title) == "" || TextUnits(step.Title) > 500 {
			return "checklist titles must contain 1 to 500 characters"
		}
	}
	if len(in.Checklist) > maxSteps {
		return "checklist has more steps than the service supports"
	}
	return ""
}
