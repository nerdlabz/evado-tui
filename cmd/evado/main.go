// Command evado is a terminal client for the Evado todo API.
//
// It ships as a single static binary with no runtime dependencies, so the
// same build runs on Linux, macOS, Windows, WSL, and anything else that
// can execute a Go binary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nerdlabz/evado-tui/internal/api"
	"github.com/nerdlabz/evado-tui/internal/config"
	"github.com/nerdlabz/evado-tui/internal/local"
	"github.com/nerdlabz/evado-tui/internal/ops"
	"github.com/nerdlabz/evado-tui/internal/tui"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	var (
		token     = flag.String("token", "", "API token, starting evt_; overrides the saved one")
		baseURL   = flag.String("server", "", "API base URL, default "+api.DefaultBaseURL)
		showVer   = flag.Bool("version", false, "print the version and exit")
		checkOnly = flag.Bool("check", false, "verify the token against the API and exit")
		saveToken = flag.Bool("save", false, "save the token from -token to the config and exit")
		poll      = flag.Int("poll", 0, "seconds between background refreshes, at least 120, default 600")
		mouse     = flag.Bool("mouse", false, "enable mouse wheel and click support")
		theme     = flag.String("theme", "", "auto, light, dark, or a theme name such as tokyonight")
		noCache   = flag.Bool("no-cache", false, "keep no todos on this computer: talk straight to the server, online only")
		onLocal   = flag.Bool("local", false, "commands only: use the todos kept on this computer, not the server, even with a token")
		demo      = flag.Bool("demo", false, "try the TUI offline with temporary sample todos; removed on exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Println("evado", version)
		return
	}

	// A missing or broken config is not fatal: the TUI prompts for a token
	// and writes a fresh one.
	var cfg config.Config
	cleanupDemo := func() {}
	if *demo {
		if *saveToken || *checkOnly || *noCache || flag.NArg() > 0 || *token != "" || *baseURL != "" {
			fatal(errors.New("-demo is for the TUI only; omit commands, -token, -server, -save, -check and -no-cache"))
		}
		var err error
		cfg, cleanupDemo, err = prepareDemo()
		if err != nil {
			fatal(err)
		}
		defer cleanupDemo()
	} else {
		cfg, _ = config.Load()
	}

	// A flag beats the environment, and the environment beats the config
	// file. EVADO_TOKEN and EVADO_SERVER are how a script or an agent is
	// pointed at an account without writing anything to disk.
	tok := firstNonEmpty(*token, os.Getenv("EVADO_TOKEN"))
	srv := firstNonEmpty(*baseURL, os.Getenv("EVADO_SERVER"))
	if *demo {
		tok, srv = "", ""
	}

	switch {
	case *saveToken:
		if *token == "" {
			fatal(errors.New("-save needs -token"))
		}
		cfg.Token = *token
		if *baseURL != "" {
			cfg.BaseURL = *baseURL
		}
		if err := cfg.Save(); err != nil {
			fatal(err)
		}
		path, err := config.Path()
		if err != nil {
			fatal(err)
		}
		fmt.Println("token saved to", path)
		return
	case *checkOnly:
		use := firstNonEmpty(tok, cfg.Token)
		if use == "" {
			fatal(errors.New("no token: pass -token, set EVADO_TOKEN, or save one first"))
		}
		c := api.New(use, api.WithBaseURL(firstNonEmpty(srv, cfg.BaseURL)))
		u, lists, err := c.Me(context.Background())
		if err != nil {
			fatal(err)
		}
		fmt.Printf("ok: %s  tz=%s  lists=%d  server=%s\n", u.Email, u.TZ, len(lists), c.BaseURL())
		return
	}

	if tok != "" {
		cfg.Token = tok
		if srv != "" {
			cfg.BaseURL = srv
		}
	} else if srv != "" {
		cfg.BaseURL = srv
	}

	if isCommand(flag.Args()) {
		set := false
		flag.Visit(func(f *flag.Flag) { set = set || f.Name == "local" })
		runCLI(cfg, flag.Args(), localMode(set, *onLocal, os.Getenv("EVADO_LOCAL"), cfg))
		return
	}
	if flag.NArg() > 0 {
		fatal(fmt.Errorf("unknown command %q (commands: %s)", flag.Arg(0), strings.Join(commandNames, ", ")))
	}

	// Per-run display options. They are not written to the config file:
	// edit it to make them permanent.
	if *poll != 0 {
		cfg.PollSeconds = *poll
	}
	if *mouse {
		cfg.Mouse = true
	}
	if *theme != "" {
		cfg.Theme = *theme
	}
	if *noCache {
		cfg.NoCache = true
	}

	// Ctrl-C inside the TUI is handled as a key, so the terminal is only
	// sent a signal when something else interrupts the process. Either
	// way the alternate screen is restored on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Mouse reporting is off unless asked for, because capturing it stops
	// the terminal selecting text.
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if cfg.Mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	program := tea.NewProgram(tui.New(ctx, cfg), opts...)
	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		cleanupDemo()
		fatal(err)
	}
}

// localMode decides whether a command works on the todos kept on this
// computer instead of the server. Asking with -local, or -local=false,
// decides it; then EVADO_LOCAL=1. Any other EVADO_LOCAL, 0 included, is the
// same as none, so a shell that sets it to 0 to mean "not forced" gets the
// rows below rather than a server it has no token for. Otherwise a token
// means the server, as it always has, and with no token the todos here are
// used only if the TUI was set up to go without an account. Anything else
// is the usual no-token error, so a script whose token went missing fails
// loudly instead of quietly writing to a file nobody reads.
func localMode(flagSet, flagVal bool, env string, cfg config.Config) bool {
	if flagSet {
		return flagVal
	}
	if on, err := strconv.ParseBool(env); err == nil && on {
		return true
	}
	return cfg.Token == "" && cfg.Local && !cfg.NoCache
}

// runCLI runs a non-interactive command and exits with a code that says how
// it went; see the ops package for what each code means.
func runCLI(cfg config.Config, args []string, onLocal bool) {
	jsonOut := wantsJSON(args, os.Getenv("EVADO_JSON"))
	var c *api.Client
	switch {
	case onLocal:
		path, err := local.Path()
		var b *local.Backend
		if err == nil {
			// No account is given to it, so nothing here reaches the
			// network. Changes to todos tied to an account wait in the file
			// for the TUI to send them, as changes made offline do.
			b, err = local.Open(path, "", "")
		}
		if err != nil {
			exitWith(err, jsonOut)
		}
		c = b.Client()
	case cfg.Token == "":
		exitWith(&api.Error{
			StatusCode: 401, Code: api.CodeUnauthorized,
			Message: "no token: pass -token, set EVADO_TOKEN, or save one with evado -save -token evt_…",
		}, jsonOut)
	default:
		c = api.New(cfg.Token, api.WithBaseURL(cfg.BaseURL))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	o := ops.New(c)

	// Standard input is only read when something is piped in, so a command
	// run by hand never sits waiting on the keyboard.
	var in io.Reader
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		in = os.Stdin
	}
	if onLocal {
		if err := serverOnly(args); err != nil {
			exitWith(err, jsonOut)
		}
	}
	if err := runCommand(ctx, o, args, os.Stdout, in, jsonOut); err != nil {
		exitWith(err, jsonOut)
	}
}

// exitWith reports an error, as JSON on standard error when asked, and
// exits with its code.
func exitWith(err error, jsonOut bool) {
	info := describe(err)
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"error": info})
		fmt.Fprintln(os.Stderr, string(b))
	} else {
		fmt.Fprintln(os.Stderr, "evado:", errorText(err))
	}
	os.Exit(info.ExitCode)
}

func usage() {
	fmt.Fprint(os.Stderr, `evado: terminal client for the Evado todo API

usage:
  evado                 start the TUI with the saved token
  evado -token evt_...  start with a token for this run only
  evado -save -token …  save a token to the config file
  evado -check          verify the saved token and exit
  evado -version        print the version
  evado -demo           try sample todos offline; changes discarded on exit

commands for scripts and agents, which do not start the TUI. Put global flags before the command:
  evado add [-list name] [-notes text] [-id id] <text | ->
  evado ls [-status s] [-list name] [-q text] [-assigned me|anyone] [-limit n]
           [-cursor c]
                  Returns up to 200 todos unless -limit says otherwise, reading as
                  many pages as that takes. If more match, a note on standard error
                  says so, and -json has "nextCursor" (null when nothing more
                  matches): pass it back as -cursor, with the same filters, to carry on.
  evado show <id>
  evado done <id>...      evado undone <id>...
  evado edit <id> [-title t] [-notes n] [-list l] [-day d] [-at t] [-due t]
                  [-remind t] [-repeat rule] [-assign email|anyone] [-done=bool]
                  Dates take ISO or words like "friday 5pm". A day alone ("friday",
                  2026-03-10) has no time: -day takes it, -at keeps a do date with no
                  time of day, -due and -remind take midnight that day. The value
                  none clears a field.
  evado claim <id>        Take a todo in a shared list, only while it is Anyone's.
  evado release <id>      Hand your todo back to Anyone, only while it is yours.
                  If someone else has it, nothing changes, the error says who and
                  the exit code is 6. Repeating a claim or release that worked
                  changes nothing.
  evado rm -yes <id>...   Without -yes, nothing is deleted.
  evado steps list <todo>            A todo's checklist, with step ids.
  evado steps add <todo> <text>      evado step <todo> <text> is the same.
  evado steps done|undone <todo> <step>   Sets the state; repeating it changes nothing.
  evado steps edit <todo> <step> <text>   Renames a step (or -title text).
  evado steps rm -yes <todo> <step>       Without -yes, nothing is deleted.
                  A step id may be shortened to a prefix that is unique on that todo.
  evado changes [-since <cursor>] [-max-pages n]
                  Everything that changed after a cursor, deleted todos included,
                  with checklists, and the latest lists and invitations. It is an
                  incremental sync feed, not an audit log and not an exactly-once
                  stream: a todo that changed twice may appear twice (the last one
                  wins), and there are no filters. Without -since it reads every
                  live todo, which is how you get a first cursor. Save "cursor"
                  from the result and pass it back unchanged as the next -since;
                  it is a position in the service's log, not a time, and the
                  command keeps no state. It
                  reads up to 50 pages of 200 by default. If that is not the end it
                  prints what it has with "complete": false and exits 10: run it
                  again from "cursor". Text output is only a summary; use -json.
  evado lists
  evado parse <text>
  evado export [file | -]           Export todos as portable JSON.
  evado import [-dry-run] [-list id] <file | ->
                                   Import an Evado export.

  Every command takes -json (or set EVADO_JSON=1). Ids may be shortened to a
  unique prefix. Exit codes: 0 ok, 2 usage, 3 auth, 4 not found, 5 rate
  limited, 6 refused by the server, 7 forbidden, 8 server error, 9 network,
  10 changes stopped at its page bound (not a failure; see above).
  Set EVADO_TOKEN and EVADO_SERVER to use an account without a config file.

  evado -local <command> works on the todos kept on this computer instead of
  the server: no token, no network. EVADO_LOCAL=1 does the same for a whole
  shell. With no token it is the default once the TUI is set up without an
  account; a token means the server unless -local is given. A running TUI
  shows the change, and sends it to the account if it is signed in. Every
  command works this way but changes, the service's feed, and claim and
  release, which only the service can decide.

flags:
`)
	flag.PrintDefaults()
	path, err := config.Path()
	if err == nil {
		fmt.Fprintf(os.Stderr, `
get a token from Settings, API & apps in your Evado account. It is stored
in %s with 0600 permissions.
`, path)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "evado:", err)
	os.Exit(1)
}
