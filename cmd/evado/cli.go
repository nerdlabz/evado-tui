package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/local"
	"github.com/nerdlabz/evado-tui/internal/ops"
)

// commandNames are the words that run a command instead of the TUI.
var commandNames = []string{"add", "ls", "show", "done", "undone", "edit", "claim", "release", "rm", "step", "steps", "changes", "lists", "parse", "import", "export"}

// isCommand reports whether the first argument names a non-interactive
// command rather than starting the TUI.
func isCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, n := range commandNames {
		if args[0] == n {
			return true
		}
	}
	return false
}

// serverOnly refuses, before anything is read, a command that only the
// service can answer, for a run against the todos on this computer. The
// change feed is the service's log of every device's changes, and the file
// here keeps no such log: answering from it would hand out a cursor the
// service would not know. A claim is the service's to judge, since someone
// else may have taken the todo first, and a run here never reaches it,
// even when the todos here are signed in.
func serverOnly(args []string) error {
	if len(args) == 0 {
		return nil
	}
	why := map[string]string{
		"changes": "changes reads the service's change feed, which the todos on this computer do not have",
		"claim":   "claim asks the service, which decides who a shared todo belongs to",
		"release": "release asks the service, which decides who a shared todo belongs to",
	}[args[0]]
	if why == "" {
		return nil
	}
	return &ops.Error{
		Code:    "needs_server",
		Message: why + "; run it with a token and without -local",
		Exit:    ops.ExitUsage,
	}
}

// wantsJSON reports whether output should be JSON: turned on for the whole
// environment with EVADO_JSON=1, which is what a script or an agent wants
// everywhere, and then as the command's own -json flag says, in any spelling
// the flag package takes (-json, --json, -json=false). It is known before the
// command runs, because an error from setting up the run, such as a missing
// token, is printed the same way. The command's own flags are parsed to find
// out, so that a -json that is the value of another flag (-notes -json) is
// read as that value here too.
func wantsJSON(args []string, env string) bool {
	c := &cli{json: env == "1" || strings.EqualFold(env, "true"), probe: true}
	if len(args) > 0 {
		_ = c.dispatch(context.Background(), args[0], args[1:])
	}
	return c.json
}

// errProbed ends a command that was run only to read its flags.
var errProbed = errors.New("flags read")

// commandHelp is what a command prints when asked for help with -h, -help
// or --help: its synopsis and its flags. Like wantsJSON it is read from the
// command's own flags before the run is set up, so help needs no token and
// opens no file, and a -h that is another flag's value (-notes -h) is not
// taken for it.
func commandHelp(args []string) (string, bool) {
	c := &cli{probe: true}
	if len(args) > 0 {
		_ = c.dispatch(context.Background(), args[0], args[1:])
	}
	return c.help, c.help != ""
}

// cli carries what a command needs.
type cli struct {
	o   *ops.Ops
	out io.Writer
	// errw is where a note goes that a script reading out should not see.
	errw io.Writer
	in   io.Reader // nil when standard input is a terminal
	json bool
	// probe stops a command once its flags are parsed, before anything else
	// is touched: o and the writers are nil.
	probe bool
	// help is set by a probe whose flags asked for help.
	help string
}

// runCommand runs one non-interactive command. It prints results to out and
// returns any error for the caller to report and turn into an exit code.
func runCommand(ctx context.Context, o *ops.Ops, args []string, out io.Writer, in io.Reader, jsonOut bool) error {
	return runCommandNoting(ctx, o, args, out, os.Stderr, in, jsonOut)
}

// runCommandNoting is runCommand with the notes a command adds beside its
// result, which are not errors, going to errw.
func runCommandNoting(ctx context.Context, o *ops.Ops, args []string, out, errw io.Writer, in io.Reader, jsonOut bool) error {
	c := &cli{o: o, out: out, errw: errw, in: in, json: jsonOut}
	return c.dispatch(ctx, args[0], args[1:])
}

// dispatch runs the named command. Every command parses its flags before
// it does anything else, which is what lets wantsJSON run one to read them.
func (c *cli) dispatch(ctx context.Context, name string, rest []string) error {
	switch name {
	case "export":
		return c.exportFile(ctx, rest)
	case "import":
		return c.importFile(ctx, rest)
	case "add":
		return c.add(ctx, rest)
	case "ls":
		return c.list(ctx, rest)
	case "show":
		return c.show(ctx, rest)
	case "done":
		return c.setDone(ctx, rest, true)
	case "undone":
		return c.setDone(ctx, rest, false)
	case "edit":
		return c.edit(ctx, rest)
	case "claim":
		return c.assignment(ctx, rest, true)
	case "release":
		return c.assignment(ctx, rest, false)
	case "rm":
		return c.remove(ctx, rest)
	case "step":
		return c.step(ctx, rest)
	case "steps":
		return c.steps(ctx, rest)
	case "changes":
		return c.changes(ctx, rest)
	case "lists":
		return c.lists(ctx, rest)
	case "parse":
		return c.parse(ctx, rest)
	}
	return fmt.Errorf("unknown command %q", name)
}

func (c *cli) importFile(ctx context.Context, args []string) error {
	fs := newFlags("import")
	var opts api.ImportOptions
	fs.BoolVar(&opts.DryRun, "dry-run", false, "preview without saving")
	fs.StringVar(&opts.ListID, "list", "", "destination list id")
	pos, err := c.parseArgs(fs, args, "import [-dry-run] [-list id] [-json] <file | ->")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &usageErr{"usage: evado import [-dry-run] [-list id] [-json] <file | ->"}
	}
	var reader io.Reader
	if pos[0] == "-" {
		if c.in == nil {
			return &usageErr{"import - needs an export on standard input"}
		}
		reader = c.in
	} else {
		f, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		reader = f
	}
	res, err := c.o.Import(ctx, reader, opts)
	if err != nil {
		return err
	}
	if c.json {
		if err := json.NewEncoder(c.out).Encode(res); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(c.out, importReceipt(res)); err != nil {
			return err
		}
		for _, r := range res.Refused {
			if _, err := fmt.Fprintf(c.out, "%s: %s\n", importRefusalName(r), r.Reason); err != nil {
				return err
			}
		}
	}
	if !res.Complete {
		message := "import is incomplete; rerun the same file explicitly to continue"
		if opts.DryRun {
			message = "preview covers one request; import the same file without -dry-run to begin"
		}
		return &ops.Error{Code: "partial_import", Message: message, Exit: ops.ExitIncomplete}
	}
	return nil
}

func importRefusalName(r api.ImportRefusal) string {
	if r.Kind != "" || r.ID != "" {
		return strings.TrimSpace(r.Kind + " " + r.ID)
	}
	return fmt.Sprintf("item %d", r.Item)
}

func importReceipt(r api.ImportResult) string {
	prefix := "import"
	if r.DryRun {
		prefix = "preview"
	}
	summary := fmt.Sprintf("%s: %d todos added, %d skipped, %d deleted skipped; %d lists added, %d used; %d refused", prefix, max(r.Added, r.TodosAdded), max(r.Skipped, r.TodosSkipped), r.SkippedDeleted, max(r.ListsAdded, len(r.Lists.Created)), max(r.ListsSkipped, len(r.Lists.Used)), max(r.RefusedCount, len(r.Refused)))
	if r.TodosUpdated > 0 {
		summary += fmt.Sprintf("; %d todos updated", r.TodosUpdated)
	}
	fields := []string{}
	for name, n := range r.Dropped {
		if n > 0 {
			fields = append(fields, fmt.Sprintf("%s (%d)", name, n))
		}
	}
	sort.Strings(fields)
	if len(fields) > 0 {
		summary += "; dropped fields: " + strings.Join(fields, ", ")
	}
	if r.ApproximateDates > 0 {
		summary += fmt.Sprintf("; %d approximate dates", r.ApproximateDates)
	}
	if !r.Complete {
		summary += fmt.Sprintf("; incomplete, %d remaining", r.Remaining)
	}
	return summary
}

func (c *cli) exportFile(ctx context.Context, args []string) error {
	fs := newFlags("export")
	pos, err := c.parseArgs(fs, args, "export [file | -]")
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return &usageErr{"usage: evado export [file | -]"}
	}
	ex, err := c.o.Export(ctx)
	if err != nil {
		return err
	}
	if len(pos) == 0 || pos[0] == "-" {
		return api.EncodeTodosExport(c.out, ex)
	}
	return local.WriteExport(pos[0], ex)
}

// -- parsing ----------------------------------------------------------------

type usageErr struct{ msg string }

func (e *usageErr) Error() string { return e.msg }

// newFlags builds a flag set for a command. Errors become usage errors that
// carry the synopsis, instead of the flag package printing its own.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs adds the -json flag every command takes to fs and parses the
// arguments with parseFlags. Registering it here, once, is what keeps
// wantsJSON and the command agreeing on it.
func (c *cli) parseArgs(fs *flag.FlagSet, args []string, synopsis string) ([]string, error) {
	fs.BoolVar(&c.json, "json", c.json, "print JSON")
	pos, err := parseFlags(fs, args, synopsis, c.probe)
	if c.probe {
		if errors.Is(err, flag.ErrHelp) {
			var b strings.Builder
			fmt.Fprintf(&b, "usage: evado %s\n\nflags:\n", synopsis)
			fs.SetOutput(&b)
			fs.PrintDefaults()
			c.help = b.String()
		}
		return nil, errProbed
	}
	return pos, err
}

// parseFlags parses flags wherever they appear among the arguments, which is
// how people and agents actually write them (evado edit abc -title x), and
// returns the positional arguments. Everything after -- is positional.
//
// With keepGoing a flag that does not parse is skipped rather than ending
// the parse, so that a -json after a mistyped flag still has the usage error
// printed as JSON. A request for help among them is reported as
// flag.ErrHelp once everything has been read.
func parseFlags(fs *flag.FlagSet, args []string, synopsis string, keepGoing bool) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	var help error
	for {
		if err := fs.Parse(args); err != nil {
			if !keepGoing {
				return nil, &usageErr{fmt.Sprintf("%v\nusage: evado %s", err, synopsis)}
			}
			if errors.Is(err, flag.ErrHelp) {
				help = err
			}
			// Bad flag syntax (---x) is refused before the flag package
			// steps past it.
			if rest := fs.Args(); len(rest) < len(args) {
				args = rest
			} else {
				args = args[1:]
			}
			continue
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return append(pos, tail...), help
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func usageOf(synopsis string) error { return &usageErr{"usage: evado " + synopsis} }

// -- output -----------------------------------------------------------------

func (c *cli) emit(v any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// write prints text, collecting the write error once.
func (c *cli) write(s string) error {
	_, err := io.WriteString(c.out, s)
	return err
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func todoLine(t api.Todo, full bool) string {
	id := short(t.ID)
	if full {
		id = t.ID
	}
	mark := "○"
	if t.Completed() {
		mark = "✓"
	}
	line := fmt.Sprintf("%s  %s  %s", id, mark, t.Title)
	switch {
	case t.ScheduledDay != nil:
		line += "  (" + *t.ScheduledDay + ")"
	case t.DueAt != nil:
		line += "  (" + t.DueAt.Format("2006-01-02") + ")"
	}
	return line
}

// -- commands ---------------------------------------------------------------

const addSynopsis = "add [-list name] [-notes text] [-id id] [-passed kept|moved] [-json] <text | ->"

func (c *cli) add(ctx context.Context, args []string) error {
	fs := newFlags("add")
	list := fs.String("list", "", "add to this list instead of your personal list")
	notes := fs.String("notes", "", "notes for the todo")
	id := fs.String("id", "", "an id of your choosing, so a retried add is recognisable")
	passed := fs.String("passed", "", passedUsage)
	pos, err := c.parseArgs(fs, args, addSynopsis)
	if err != nil {
		return err
	}
	text := strings.Join(pos, " ")
	if len(pos) == 0 || text == "-" {
		if c.in == nil {
			return usageOf(addSynopsis)
		}
		b, rerr := io.ReadAll(c.in)
		if rerr != nil {
			return rerr
		}
		text = string(b)
	}
	todos, err := c.o.Add(ctx, ops.AddInput{Text: text, List: *list, Notes: *notes, ID: *id, TimePassed: *passed})
	if err != nil && len(todos) == 0 {
		return err
	}
	// Long input goes up in batches, and a failed batch leaves the earlier
	// ones added: print those before the error so nothing is added twice.
	if c.json {
		if eerr := c.emit(map[string]any{"todos": todos}); eerr != nil {
			return eerr
		}
		return err
	}
	var b strings.Builder
	for _, t := range todos {
		fmt.Fprintf(&b, "added %s  %s\n", short(t.ID), t.Title)
	}
	if werr := c.write(b.String()); werr != nil {
		return werr
	}
	return err
}

const listSynopsis = "ls [-status s] [-list name] [-q text] [-assigned me|anyone] [-limit n] [-cursor c] [-full-ids] [-json]"

func (c *cli) list(ctx context.Context, args []string) error {
	fs := newFlags("ls")
	status := fs.String("status", "", "open, today, overdue, upcoming, due, completed or all; default open")
	list := fs.String("list", "", "only this list")
	query := fs.String("q", "", "search titles and notes")
	assigned := fs.String("assigned", "", "me or anyone")
	limit := fs.Int("limit", 0, "most todos to return, default 200; more than 200 is read a page at a time")
	cursor := fs.String("cursor", "", "carry on after the todos an earlier ls stopped at: its nextCursor, with the same filters")
	full := fs.Bool("full-ids", false, "print whole ids instead of the first eight characters")
	pos, err := c.parseArgs(fs, args, listSynopsis)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageOf(listSynopsis)
	}
	// 0 is the default; a negative limit is a mistake, and would otherwise
	// quietly mean 200 like the default does.
	if *limit < 0 {
		return &usageErr{fmt.Sprintf("-limit must be 0 or more, not %d\nusage: evado %s", *limit, listSynopsis)}
	}
	found, err := c.o.ListTodos(ctx, ops.ListInput{
		Status: *status, List: *list, Query: *query, Assigned: *assigned, Limit: *limit, Cursor: *cursor,
	})
	if err != nil {
		return err
	}
	if c.json {
		// nextCursor is added beside count and todos, so a script that reads
		// those two keeps working. It is null when nothing more matches.
		var next *string
		if found.NextCursor != "" {
			next = &found.NextCursor
		}
		return c.emit(map[string]any{"count": len(found.Todos), "todos": found.Todos, "nextCursor": next})
	}
	var b strings.Builder
	for _, t := range found.Todos {
		b.WriteString(todoLine(t, *full) + "\n")
	}
	if err := c.write(b.String()); err != nil {
		return err
	}
	if found.NextCursor != "" {
		// The command still succeeds: a script that ran `ls` before there
		// were more than 200 todos must not start failing now. The note goes
		// beside the list, not in it.
		_, err = fmt.Fprintf(c.errw, "evado: %d shown and more match; raise -limit, or carry on with -cursor %s\n", len(found.Todos), found.NextCursor)
	}
	return err
}

const showSynopsis = "show [-json] <id>"

func (c *cli) show(ctx context.Context, args []string) error {
	fs := newFlags("show")
	pos, err := c.parseArgs(fs, args, showSynopsis)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageOf(showSynopsis)
	}
	d, err := c.o.Show(ctx, pos[0])
	if err != nil {
		return err
	}
	if c.json {
		if d.Steps == nil {
			d.Steps = []api.ChecklistItem{}
		}
		return c.emit(d)
	}
	t := d.Todo
	var b strings.Builder
	doDate := stamp(t.DoAt)
	if t.DoAt != nil && !t.HasTime {
		doDate = t.DoAt.Local().Format("2006-01-02")
		if t.ScheduledDay != nil {
			doDate = *t.ScheduledDay
		}
	}
	b.WriteString(todoLine(t, true) + "\n")
	fmt.Fprintf(&b, "  list      %s\n", d.ListName)
	for _, row := range []struct{ k, v string }{
		{"day", deref(t.ScheduledDay)},
		{"do at", doDate},
		{"due", stamp(t.DueAt)},
		{"remind", stamp(t.RemindAt)},
		{"repeat", deref(t.RecurrenceRule)},
		{"assignee", deref(t.AssigneeID)},
	} {
		if row.v != "" {
			fmt.Fprintf(&b, "  %-9s %s\n", row.k, row.v)
		}
	}
	if n := deref(t.Notes); n != "" {
		fmt.Fprintf(&b, "  notes     %s\n", strings.ReplaceAll(n, "\n", "\n            "))
	}
	for _, s := range d.Steps {
		mark := "□"
		if s.Completed() {
			mark = "☑"
		}
		fmt.Fprintf(&b, "  %s %s\n", mark, s.Title)
	}
	return c.write(b.String())
}

const doneSynopsis = "done|undone [-json] <id>..."

func (c *cli) setDone(ctx context.Context, args []string, done bool) error {
	fs := newFlags("done")
	pos, err := c.parseArgs(fs, args, doneSynopsis)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageOf(doneSynopsis)
	}
	var results []ops.Change
	var b strings.Builder
	var fails batch
	// One listing serves every short id in the batch.
	r := c.o.Resolver()
	for _, ref := range pos {
		ch, err := c.o.SetDoneWith(ctx, r, ref, done)
		if err != nil {
			fails.add(ref, err)
			continue
		}
		results = append(results, ch)
		verb := map[bool]string{true: "done", false: "reopened"}[done]
		if !ch.Changed {
			verb = "already " + verb
		}
		fmt.Fprintf(&b, "%s %s  %s\n", verb, short(ch.Todo.ID), ch.Todo.Title)
	}
	if c.json {
		if results == nil {
			results = []ops.Change{}
		}
		if err := c.emit(map[string]any{"results": results, "errors": fails.infos()}); err != nil {
			return err
		}
		return fails.err()
	}
	if err := c.write(b.String()); err != nil {
		return err
	}
	return fails.err()
}

const editSynopsis = "edit <id> [-title t] [-notes n] [-list name] [-day d] [-at t] [-due t] [-remind t] [-repeat rule] [-assign email|anyone] [-done=true|false] [-json]   (use none to clear a field)"

func (c *cli) edit(ctx context.Context, args []string) error {
	fs := newFlags("edit")
	vals := map[string]*string{}
	for _, f := range [][2]string{
		{"title", "the new title"},
		{"notes", "the new notes; none clears them"},
		{"list", "move it to this list"},
		{"day", "the day it is planned for, with no time; none clears it"},
		{"at", "the do date, with a time or a day alone; none clears it"},
		{"due", "the deadline; none clears it"},
		{"remind", "when to send a reminder; none clears it"},
		{"repeat", "a repeat such as \"every monday\"; none clears it"},
		{"assign", "a list member's email, or anyone"},
	} {
		vals[f[0]] = fs.String(f[0], "", f[1])
	}
	done := fs.Bool("done", false, "true marks it done and false reopens it")
	pos, err := c.parseArgs(fs, args, editSynopsis)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageOf(editSynopsis)
	}
	// A flag that was given, even as an empty string, is a change; one that
	// was not is left alone. The flag package cannot say which is which, so
	// ask it what was visited.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	pick := func(name string) *string {
		if !set[name] {
			return nil
		}
		return vals[name]
	}
	in := ops.EditInput{
		Title: pick("title"), Notes: pick("notes"), List: pick("list"), Day: pick("day"),
		At: pick("at"), Due: pick("due"), Remind: pick("remind"), Repeat: pick("repeat"),
		Assign: pick("assign"),
	}
	if set["done"] {
		in.Done = done
	}
	ch, err := c.o.Edit(ctx, pos[0], in)
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(ch)
	}
	return c.write(fmt.Sprintf("saved %s  %s\n", short(ch.Todo.ID), ch.Todo.Title))
}

const claimSynopsis = "claim|release [-json] <id>   (shared lists only)"

// assignment takes a todo in a shared list, or hands it back to Anyone.
// Losing to someone else is an error with exit code 6 that says who has it.
func (c *cli) assignment(ctx context.Context, args []string, claim bool) error {
	name := map[bool]string{true: "claim", false: "release"}[claim]
	fs := newFlags(name)
	pos, err := c.parseArgs(fs, args, claimSynopsis)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageOf(claimSynopsis)
	}
	var ch ops.Change
	if claim {
		ch, err = c.o.Claim(ctx, pos[0])
	} else {
		ch, err = c.o.Release(ctx, pos[0])
	}
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(ch)
	}
	verb := map[bool]string{true: "claimed", false: "released to Anyone"}[claim]
	if !ch.Changed {
		verb = map[bool]string{true: "already yours", false: "already Anyone's"}[claim]
	}
	return c.write(fmt.Sprintf("%s %s  %s\n", verb, short(ch.Todo.ID), ch.Todo.Title))
}

const rmSynopsis = "rm -yes [-json] <id>...   (without -yes nothing is deleted)"

func (c *cli) remove(ctx context.Context, args []string) error {
	fs := newFlags("rm")
	yes := fs.Bool("yes", false, "really delete")
	pos, err := c.parseArgs(fs, args, rmSynopsis)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageOf(rmSynopsis)
	}
	var deleted []map[string]string
	var b strings.Builder
	var fails batch
	r := c.o.Resolver()
	for _, ref := range pos {
		t, err := c.o.DeleteWith(ctx, r, ref, *yes)
		if err != nil {
			fails.add(ref, err)
			continue
		}
		deleted = append(deleted, map[string]string{"id": t.ID, "title": t.Title})
		fmt.Fprintf(&b, "deleted %s  %s\n", short(t.ID), t.Title)
	}
	if c.json {
		if deleted == nil {
			deleted = []map[string]string{}
		}
		if err := c.emit(map[string]any{"deleted": deleted, "errors": fails.infos()}); err != nil {
			return err
		}
		return fails.err()
	}
	if err := c.write(b.String()); err != nil {
		return err
	}
	return fails.err()
}

const stepSynopsis = "step [-json] <id> <text>   (shorthand for steps add)"

func (c *cli) step(ctx context.Context, args []string) error {
	fs := newFlags("step")
	pos, err := c.parseArgs(fs, args, stepSynopsis)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usageOf(stepSynopsis)
	}
	return c.addStep(ctx, pos[0], strings.Join(pos[1:], " "))
}

func (c *cli) addStep(ctx context.Context, todo, text string) error {
	item, err := c.o.AddStep(ctx, todo, text)
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(map[string]any{"step": item})
	}
	return c.write(fmt.Sprintf("added step  %s  %s\n", short(item.ID), item.Title))
}

const stepsSynopsis = `steps list [-full-ids] [-json] <todo>
       evado steps add [-json] <todo> <text>
       evado steps done|undone [-json] <todo> <step>
       evado steps edit [-title t] [-json] <todo> <step> [text]
       evado steps rm -yes [-json] <todo> <step>   (without -yes nothing is deleted)`

// steps runs a checklist subcommand. A step is named by the todo it is on
// and its own id, which may be shortened to a prefix that is unique within
// that todo's checklist.
func (c *cli) steps(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageOf(stepsSynopsis)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return c.stepsList(ctx, rest)
	case "add":
		return c.stepsAdd(ctx, rest)
	case "done":
		return c.stepsSetDone(ctx, rest, true)
	case "undone":
		return c.stepsSetDone(ctx, rest, false)
	case "edit":
		return c.stepsEdit(ctx, rest)
	case "rm":
		return c.stepsRemove(ctx, rest)
	}
	// Only for -json and -h, so the refusal is printed the way it was asked
	// for and "steps -h" lists the steps commands.
	if strings.HasPrefix(sub, "-") {
		rest = args
	}
	_, _ = c.parseArgs(newFlags("steps"), rest, stepsSynopsis)
	return &usageErr{fmt.Sprintf("unknown steps command %q\nusage: evado %s", sub, stepsSynopsis)}
}

func (c *cli) stepsList(ctx context.Context, args []string) error {
	const syn = "steps list [-full-ids] [-json] <todo>"
	fs := newFlags("steps list")
	full := fs.Bool("full-ids", false, "print whole step ids instead of a short form")
	pos, err := c.parseArgs(fs, args, syn)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageOf(syn)
	}
	list, err := c.o.Steps(ctx, pos[0])
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(map[string]any{"todo": list.Todo, "count": len(list.Steps), "steps": list.Steps})
	}
	ids := stepIDs(list.Steps, *full)
	var b strings.Builder
	for i, s := range list.Steps {
		fmt.Fprintf(&b, "%s  %s  %s\n", ids[i], stepMark(s), s.Title)
	}
	return c.write(b.String())
}

// stepIDs are the ids a listing prints. A short id is the first eight
// characters, longer where that would leave two steps of the checklist
// indistinguishable, so what is printed can always be passed back.
func stepIDs(items []api.ChecklistItem, full bool) []string {
	out := make([]string, len(items))
	for i, s := range items {
		n := 8
		for !full && n < len(s.ID) && sharesPrefix(items, i, n) {
			n++
		}
		out[i] = s.ID
		if !full {
			out[i] = s.ID[:min(n, len(s.ID))]
		}
	}
	return out
}

// sharesPrefix reports whether another step starts with the first n
// characters of step i.
func sharesPrefix(items []api.ChecklistItem, i, n int) bool {
	id := items[i].ID
	for j, o := range items {
		if j != i && strings.HasPrefix(o.ID, id[:n]) {
			return true
		}
	}
	return false
}

func stepMark(s api.ChecklistItem) string {
	if s.Completed() {
		return "☑"
	}
	return "□"
}

func (c *cli) stepsAdd(ctx context.Context, args []string) error {
	const syn = "steps add [-json] <todo> <text>"
	fs := newFlags("steps add")
	pos, err := c.parseArgs(fs, args, syn)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usageOf(syn)
	}
	return c.addStep(ctx, pos[0], strings.Join(pos[1:], " "))
}

func (c *cli) stepsSetDone(ctx context.Context, args []string, done bool) error {
	name := map[bool]string{true: "done", false: "undone"}[done]
	syn := "steps " + name + " [-json] <todo> <step>"
	fs := newFlags("steps " + name)
	pos, err := c.parseArgs(fs, args, syn)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageOf(syn)
	}
	ch, err := c.o.SetStepDone(ctx, pos[0], pos[1], done)
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(ch)
	}
	verb := map[bool]string{true: "done", false: "reopened"}[done]
	if !ch.Changed {
		verb = "already " + verb
	}
	return c.write(fmt.Sprintf("%s step  %s  %s\n", verb, short(ch.Step.ID), ch.Step.Title))
}

func (c *cli) stepsEdit(ctx context.Context, args []string) error {
	const syn = "steps edit [-title t] [-json] <todo> <step> [text]"
	fs := newFlags("steps edit")
	title := fs.String("title", "", "the new title; the words after the step id work too")
	pos, err := c.parseArgs(fs, args, syn)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usageOf(syn)
	}
	text := strings.Join(pos[2:], " ")
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "title" })
	switch {
	case given && text != "":
		return &usageErr{"give the new title with -title or as words after the step id, not both\nusage: evado " + syn}
	case given:
		text = *title
	case text == "":
		return usageOf(syn)
	}
	ch, err := c.o.RenameStep(ctx, pos[0], pos[1], text)
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(ch)
	}
	return c.write(fmt.Sprintf("saved step  %s  %s\n", short(ch.Step.ID), ch.Step.Title))
}

func (c *cli) stepsRemove(ctx context.Context, args []string) error {
	const syn = "steps rm -yes [-json] <todo> <step>   (without -yes nothing is deleted)"
	fs := newFlags("steps rm")
	yes := fs.Bool("yes", false, "really delete")
	pos, err := c.parseArgs(fs, args, syn)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageOf(syn)
	}
	ch, err := c.o.DeleteStep(ctx, pos[0], pos[1], *yes)
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(map[string]any{"deleted": map[string]string{"id": ch.Step.ID, "todoId": ch.Step.TodoID, "title": ch.Step.Title}})
	}
	return c.write(fmt.Sprintf("deleted step  %s  %s\n", short(ch.Step.ID), ch.Step.Title))
}

const changesSynopsis = "changes [-since <cursor>] [-max-pages n] [-json]"

// feedFilterFlags are the listing filters the change feed ignores. They are
// taken so that giving one is refused, rather than quietly returning
// everything to a caller who thinks the result is narrowed.
var feedFilterFlags = []string{"status", "list", "q", "assigned", "limit"}

func (c *cli) changes(ctx context.Context, args []string) error {
	fs := newFlags("changes")
	since := fs.String("since", "", "read changes made after this cursor, the \"cursor\" an earlier run printed; leave out to read everything")
	maxPages := fs.Int("max-pages", ops.DefaultFeedPages, "most pages to read, 200 changes each")
	for _, name := range feedFilterFlags {
		fs.String(name, "", "refused: the feed takes no filters")
	}
	pos, err := c.parseArgs(fs, args, changesSynopsis)
	if err != nil {
		return err
	}
	var filtered []string
	fs.Visit(func(f *flag.Flag) {
		if slices.Contains(feedFilterFlags, f.Name) {
			filtered = append(filtered, "-"+f.Name)
		}
	})
	if len(filtered) > 0 {
		return &usageErr{fmt.Sprintf("changes takes no %s: the change feed covers every todo in every list and ignores filters\nusage: evado %s", strings.Join(filtered, ", "), changesSynopsis)}
	}
	if len(pos) > 0 {
		return usageOf(changesSynopsis)
	}
	if *maxPages < 1 {
		return &usageErr{fmt.Sprintf("-max-pages must be at least 1, not %d\nusage: evado %s", *maxPages, changesSynopsis)}
	}
	res, err := c.o.Changes(ctx, ops.ChangesInput{Since: *since, MaxPages: *maxPages})
	if err != nil {
		return err
	}
	// A result cut short is still printed, so the work is not lost, but it
	// says so in the output and in the exit code.
	if c.json {
		if err := c.emit(res); err != nil {
			return err
		}
		return res.Err()
	}
	deleted := 0
	for _, t := range res.Todos {
		if t.Deleted {
			deleted++
		}
	}
	var b strings.Builder
	from := "the start"
	if res.Since != "" {
		from = "cursor " + res.Since
	}
	fmt.Fprintf(&b, "%d changes (%d deleted) in %d pages since %s\n", len(res.Todos), deleted, res.Pages, from)
	fmt.Fprintf(&b, "lists %s, invitations %s\n", snapshotCount(len(res.Lists), res.Lists == nil), snapshotCount(len(res.Invitations), res.Invitations == nil))
	if res.Complete {
		fmt.Fprintf(&b, "complete; next -since %s\n", res.Cursor)
	} else {
		fmt.Fprintf(&b, "incomplete; resume with -since %s\n", res.Cursor)
	}
	if err := c.write(b.String()); err != nil {
		return err
	}
	return res.Err()
}

// snapshotCount is how many a snapshot held, or that the server sent none.
func snapshotCount(n int, absent bool) string {
	if absent {
		return "not sent"
	}
	return strconv.Itoa(n)
}

const listsSynopsis = "lists [-json]"

func (c *cli) lists(ctx context.Context, args []string) error {
	fs := newFlags("lists")
	pos, err := c.parseArgs(fs, args, listsSynopsis)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageOf(listsSynopsis)
	}
	lists, err := c.o.Lists(ctx)
	if err != nil {
		return err
	}
	if c.json {
		if lists == nil {
			lists = []api.List{}
		}
		return c.emit(map[string]any{"lists": lists})
	}
	var b strings.Builder
	for _, l := range lists {
		note := l.Role
		switch {
		case l.IsPersonal:
			note = "personal"
		case l.ReadOnly:
			note += ", read-only"
		}
		fmt.Fprintf(&b, "%s  %s  (%s)\n", short(l.ID), l.Name, note)
	}
	return c.write(b.String())
}

const parseSynopsis = "parse [-passed kept|moved] [-json] <text>"

// passedUsage describes -passed, which add and parse share.
const passedUsage = "for a time in the text that has already passed today: kept keeps it on today with no reminder, moved moves it to tomorrow; without it \"today\" or \"tonight\" keeps and a time with no day moves"

func (c *cli) parse(ctx context.Context, args []string) error {
	fs := newFlags("parse")
	passed := fs.String("passed", "", passedUsage)
	pos, err := c.parseArgs(fs, args, parseSynopsis)
	if err != nil {
		return err
	}
	pv, err := c.o.ParseChoosing(ctx, strings.Join(pos, " "), *passed)
	if err != nil {
		return err
	}
	if c.json {
		return c.emit(map[string]any{"parse": pv})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "title   %s\n", pv.Title)
	if pv.ScheduledDay != nil {
		fmt.Fprintf(&b, "day     %s\n", *pv.ScheduledDay)
	}
	if pv.DoAt != nil {
		fmt.Fprintf(&b, "do at   %s\n", stamp(pv.DoAt))
	}
	if pv.DueAt != nil {
		fmt.Fprintf(&b, "due     %s\n", stamp(pv.DueAt))
	}
	if a := pv.AssumedTime; a != nil {
		fmt.Fprintf(&b, "assumed %q was read as %s", a.Text, strings.ToUpper(a.Assumed))
		if hint := a.Hint(); hint != "" {
			b.WriteString("; " + hint)
		}
		b.WriteString("\n")
	}
	switch pv.Passed() {
	case api.TimeKept:
		b.WriteString("passed  that time has passed, so it stays on today with no reminder; -passed moved gives tomorrow\n")
	case api.TimeMoved:
		b.WriteString("passed  that time has passed today, so it is for tomorrow; -passed kept keeps it on today\n")
	}
	if pv.ScheduledDay == nil && pv.DoAt == nil && pv.DueAt == nil {
		b.WriteString("no date: it would go to the inbox\n")
	}
	return c.write(b.String())
}

// -- several ids at once -----------------------------------------------------

// batch collects the failures of a command that acts on several ids, so the
// ones that worked are still done and reported, and the caller gets one
// error whose exit code is the first failure's.
type batch struct {
	refs []string
	errs []error
}

func (b *batch) add(ref string, err error) {
	b.refs = append(b.refs, ref)
	b.errs = append(b.errs, err)
}

// err is nil when nothing failed, the failure itself when only one did, and
// otherwise an error that names them all.
func (b *batch) err() error {
	switch len(b.errs) {
	case 0:
		return nil
	case 1:
		return b.errs[0]
	}
	return &batchErr{refs: b.refs, errs: b.errs}
}

// infos are the failures in the shape JSON output carries.
func (b *batch) infos() []map[string]any {
	out := []map[string]any{}
	for i, err := range b.errs {
		out = append(out, map[string]any{"ref": b.refs[i], "error": describe(err)})
	}
	return out
}

type batchErr struct {
	refs []string
	errs []error
}

func (e *batchErr) Error() string {
	lines := make([]string, len(e.errs))
	for i, err := range e.errs {
		lines[i] = e.refs[i] + ": " + err.Error()
	}
	return strings.Join(lines, "\n")
}

// Unwrap gives the first failure, which decides the exit code.
func (e *batchErr) Unwrap() error { return e.errs[0] }

// -- small helpers ----------------------------------------------------------

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04 MST")
}

// describe is ops.Describe that also knows about usage errors, which are
// the command line's own.
func describe(err error) ops.Info {
	var ue *usageErr
	if errors.As(err, &ue) {
		return ops.Info{Code: "usage", Message: ue.msg, ExitCode: ops.ExitUsage}
	}
	return ops.Describe(err)
}

// errorText is how a failure is printed in plain mode.
func errorText(err error) string {
	var be *batchErr
	if errors.As(err, &be) {
		return be.Error()
	}
	info := describe(err)
	if info.Code == "usage" {
		return info.Message
	}
	var b strings.Builder
	b.WriteString(info.Message)
	if info.RequestID != "" {
		fmt.Fprintf(&b, " [request %s]", info.RequestID)
	}
	if info.RetryAfter > 0 {
		fmt.Fprintf(&b, " (retry in %ds)", info.RetryAfter)
	}
	for _, c := range info.Candidates {
		b.WriteString("\n  " + c)
	}
	return b.String()
}
