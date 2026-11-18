# evado-tui

A terminal client for the [Evado](https://evado.app) todo API.

One static binary, no runtime dependencies. The same build runs on Linux,
macOS, Windows, WSL, a Raspberry Pi, or a container.

```
 ◆ evado   Todos   Lists   Sync   Account              ━━━━━━━━━━ 2/4  5 open · 1 today · 1 overdue
╭─ Views ────────────────╮╭─ Everything ───────────────────────────────────────────────────────────╮
│ ◆ Everything         5 ││ Overdue ────────────────────────────────────────────────────────────── │
│ ▲ Overdue            1 ││▌○ Dentist appointment                                 Mon 28 Sep 11:30 │
│ ● Today              1 ││ Today ──────────────────────────────────────────────────────────────── │
│ ◇ Upcoming           3 ││ ○ Pay the cable bill                              ≡ ◷      Today 19:00 │
│ ✓ Completed          2 ││ Tomorrow ───────────────────────────────────────────────────────────── │
│                        ││ ○ Water the plants                                  ↻         Tomorrow │
│ LISTS                  ││ Later 2 ────────────────────────────────────────────────────────────── │
│ ○ All lists            ││ ○ Review the quarterly plan                                  Fri 2 Oct │
│ ● Personal           2 ││ ○ Renew the passport                                @  Sat 3 Oct 14:00 │
│ ● Household          2 ││ Done 2 ─────────────────────────────────────────────────────────────── │
│ ⊘ Work               1 ││ ✓ Buy oat milk and coffee                                              │
│                        ││ ✓ Book the flights                                                     │
│                        ││                                                                        │
│                        ││                                                                        │
│                        ││                                                                        │
│  DONE · 14 DAYS        ││                                                                        │
│  ▁▁▁▁▁▁▁▁▁▁▁▁▁█ 2      ││                                                                        │
│  ✓ 2 done today        ││                                                                        │
╰────────────────────────╯╰────────────────────────────────────────────────────────────── 7 todos ─╯
 j/k move  enter open  space complete  n new  e edit  t/T/w reschedule  < > day  : commands  ? help
```

The Todos tab has a sidebar of views and lists with live counts, this month
at a glance, and what you finished over the last two weeks. On a wide
terminal the todo under the cursor opens alongside the list. `b` turns the
list into a board with a column per day, where `<` and `>` move a todo from
one day to the next. `ctrl+t` picks a theme.

Combinations you come back to can be saved under a name of your own. Set up
the list, status, person, search and layout, press `s`, and the view appears
under SAVED in the sidebar and in the command palette. See Saved views below.

## Getting started

### Install

Homebrew, on macOS and Linux:

```sh
brew install nerdlabz/tap/evado
```

Nix, with flakes enabled:

```sh
nix run 'github:nerdlabz/evado-tui?dir=packaging/nix'
nix profile install 'github:nerdlabz/evado-tui?dir=packaging/nix#evado'
```

On NixOS or with Home Manager, add the flake as an input:

```nix
{
  inputs.evado.url = "github:nerdlabz/evado-tui?dir=packaging/nix";
  inputs.evado.inputs.nixpkgs.follows = "nixpkgs";
}
```

and put `inputs.evado.packages.${pkgs.stdenv.hostPlatform.system}.evado` in
`environment.systemPackages` or `home.packages`. To build from source instead,
use the root flake: `nix build github:nerdlabz/evado-tui`.

Otherwise, download the archive for your platform from
[Releases](https://github.com/nerdlabz/evado-tui/releases), check it against
`SHA256SUMS`, and put `evado` somewhere on your `PATH`. Builds are published
for macOS, Linux and Windows, on ARM64 and x86-64, and for 32-bit ARM Linux.
The macOS binaries are signed and notarized.
With Go 1.25 or later, `go install github.com/nerdlabz/evado-tui/cmd/evado@latest`
works too.

### First run

Run `evado -demo` to try sample todos offline. It uses a temporary profile,
ignores saved and environment credentials, and removes its changes when
you quit. Demo mode is for the TUI only; commands use `-local` or an account.

You can start without an account. Run `evado`, choose "Use without an
account" on the first screen, and your todos are kept in a file on this
computer. When you later sign in from Account, Connection, they are added to
your account and kept in sync from then on.

The first screen lists the ways to start. To sign in straight away, press
enter on the first one. Evado opens your browser, where you can sign in or
create an account and approve Evado TUI. The terminal shows a clickable link
if the browser cannot open. Escape cancels; an unanswered sign-in expires
after five minutes. Browser sign-in also allows account deletion from the
Account tab. Exporting todos needs only permission to read todos.

On a machine without a browser, choose "Use an email code or an API token"
and type your existing account email for a six-digit code, or paste an API
token; typing or pasting on the list opens that field too. `ctrl+b` starts
browser sign-in from anywhere on the screen. The server's address is
shown only when it was set to another one, as with `-server`. The
session renews itself and is kept in `session.json` next to the config, with
`0600` permissions.

You can also paste an API token from Settings, API & apps in your Evado
account. It starts with `evt_`, and scripts and the CLI commands use it.

Account, Export saves an Evado JSON file containing lists, todos, notes and
checklists, including completed todos. Deleted todos, assignees, list members
and account records are left out. Account, Import previews an Evado file before
asking you to bring it in. Existing todos are always skipped, including deleted
ones, and sharing is not copied. Past reminders and reminders on completed
todos are left off.

The CLI uses the same file for a server account or, with `-local`, the todos on
this computer:

```sh
evado export todos.json
evado import -dry-run todos.json
evado import todos.json
evado -local export > todos.json
evado -local import todos.json
```

`export` without a path writes JSON to standard output; named files use `0600`
permissions on Unix. `import -` reads standard input. `import -list <id>` puts
the todos into one writable list. `-json` prints the import report for scripts.
A server import that stops at its request limit prints its report and exits
with code 10; run it again with the same file to continue. Neither the CLI nor
the TUI retries a partial import automatically.

Files use the `evado-export` version 1 header. Older Evado exports and full
account exports can also be imported; unknown fields are ignored, and an
unsupported format or version is refused. Foreign todo-app files are imported
through the web app. The full copy of account records remains available there.

The next save upgrades the local todos file to version 2, which keeps import
identities across sync and deletion. This version reads older files; older
binaries refuse the upgraded file rather than lose that information.

```sh
mise trust                 # dev environment: Go, gofumpt, golangci-lint
mise run build             # -> dist/evado

dist/evado -token evt_…    # try it for this run only
dist/evado -save -token evt_…   # save it to the config file
dist/evado                 # start the TUI
```

`evado -check` verifies the saved token against the API and exits, which is
useful in a script or a container health check.

| flag | meaning |
| --- | --- |
| `-token` | use this token for this run |
| `-save` | save the token from `-token` to the config file |
| `-server` | point at a different API root, such as a test server |
| `-check` | verify the token and print the account, then exit |
| `-poll` | seconds between background refreshes, at least 120, default 600 |
| `-mouse` | turn on wheel scrolling and click-to-select |
| `-no-cache` | keep no todos on this computer: talk straight to the server, online only |
| `-local` | commands only: use the todos kept on this computer instead of the server |
| `-theme` | `auto`, `light`, `dark`, or a theme name such as `tokyonight` |
| `-version` | print the version |

Passing `-token` is for that run only: remembering your last view never
writes it to the config file.

### From a script or an agent

Commands run without the TUI: `add`, `ls`, `show`, `done`, `undone`, `edit`,
`claim`, `release`, `rm`, `step`, `steps`, `changes`, `lists`, `parse`,
`export` and `import`.
They suit shell aliases, cron and any agent that can run a command, and they
use the same token and server as the app, or `EVADO_TOKEN` and `EVADO_SERVER`.
Agents that speak MCP should use the service's own MCP endpoint, described in
[AGENTS.md](AGENTS.md).

```sh
evado add "Call the bank tomorrow 3pm"      # dates are parsed server-side
evado ls -status today -json
evado ls -status all -limit 1000             # 200 is the default; more is read page by page
evado edit 3fa9c1 -due friday -assign sam@example.com
evado claim 3fa9c1                           # "I'll do it", unless someone already has
evado rm -yes 3fa9c1                         # nothing is deleted without -yes
```

Every command takes `-json`, ids can be shortened to a unique prefix, and
exit codes say what went wrong. `ls` returns 200 todos unless `-limit` says
otherwise, and says when more match. `evado help` lists the commands, and
`evado <command> -h` prints one command's flags. [AGENTS.md](AGENTS.md) lists
the commands and codes in full.

The same commands work on the todos the app keeps on this computer, with no
token and no connection:

```sh
evado -local add "Water the plants friday"
EVADO_LOCAL=1 evado ls                       # the same, for a whole shell
```

With no token at all they do this by themselves once you have chosen to use
the app without an account. A token, from `-token`, `EVADO_TOKEN` or the
config file, always means the server unless you ask for `-local`. A running
TUI shows a change made this way within a few seconds, and if this computer
is signed in it sends the change to the account like one made in the app.
`changes` is the service's change feed, and only the service can decide a
`claim` or `release`, so those three need the server.

### Config file

`config.json` also takes these optional settings:

```json
{
  "pollSeconds": 600,
  "theme": "auto",
  "mouse": false,
  "noCache": false,
  "keys": { "J": "j", "K": "k" }
}
```

- `pollSeconds` sets how often the open view refreshes itself in the
  background. The default is ten minutes, and anything below 120 seconds is
  raised to 120. The app skips a refresh while a form or dialog is open or a
  request is running, and a refresh never shows the busy indicator.
- `noCache` keeps no todos on this computer. The TUI then talks straight to
  the server and needs a connection for everything; see Offline first below.
- `local` records that you chose to use the TUI without an account.
- `theme` is `auto` to follow the terminal, `light` or `dark` to force the
  house palette, or a built-in theme: `evado`, `evado-light`, `catppuccin`,
  `tokyonight`, `gruvbox`, `nord`, `dracula` or `rosepine`. `ctrl+t` inside
  the app previews each one and saves the one you keep.
- `board` and `hideSidebar` remember whether the Todos tab was left as a
  board and with its sidebar folded away.
- `savedViews` holds the views you saved, in one set for each server and
  account, and one for use without an account. See Saved views below.
- `mouse` turns on wheel scrolling, click to select a todo, double click to
  open it, and click on a tab or a sidebar entry. It is off by default because capturing the
  mouse stops the terminal selecting text.
- `keys` remaps keys in the main views: pressing the key on the left does
  what the key on the right does. Names are single characters, `space`,
  `enter`, `esc`, `tab`, arrows, `pgup`/`pgdown`, `home`/`end` and
  `ctrl+a` to `ctrl+z`. Text entry is never remapped.

Environment overrides:

| variable | effect |
| --- | --- |
| `EVADO_CONFIG` | config file path |
| `XDG_CONFIG_HOME` | config directory (default `~/.config/evado`) |
| `EVADO_DATA_DIR` | where account exports are written |
| `NO_COLOR` | plain text, no colour |

The app stores the token in `config.json` with `0600` permissions and writes
it through a temporary file, so an interrupted save cannot corrupt it. A
`config.json` that cannot be read, after a hand edit gone wrong for example,
is never written over: the app, `-save`, `-check` and every command stop
with an error naming the file until it is fixed or moved aside.

## Keys

Press `?` inside the app for the full list. The short version:

| where | keys |
| --- | --- |
| Everywhere | `1`…`4` switch tab, `tab` next tab, `r` refresh, `/` search, `:` or `ctrl+p` command palette, `ctrl+t` theme, `q` quit |
| Todos | `j`/`k` move, `enter` open, `space` complete, `n` new, `N` quick capture (one per line), `e` edit, `f` cycle view, `a` cycle assignee (everyone, me, unassigned), `@` pick one person, `U` only undated todos, `F` pick a list, `s` save the filters as a view, `S` open, rename, update or remove a view, `u` jump to the inbox, `p` preview date parsing, `d` delete, `z` undo, `v` mark, `m` move marked, `L` load more, `t`/`T`/`w` reschedule to today, tomorrow or next week, `<`/`>` move a day earlier or later, `b` board, `h` sidebar, `[`/`]` previous or next list, `\` hide the sidebar, `y`/`Y` copy title or id, `i` in a shared list take the todo or hand yours back to Anyone |
| Forms | `enter` save, `ctrl+s` save from a notes box, `ctrl+e` open a notes box in `$VISUAL` or `$EDITOR`, `esc` cancel |
| Todo detail | `tab` checklist, `c` add step, `space` tick, `e` rename step, `d` delete step, `A` assign, `i` take or hand back (shared lists), `y` copy id |
| Lists | `n` new, `e` rename, `d` delete, `m` show members, `M` manage members, `a` invite by username or email, or pick someone you share a list with, `i` 7-day invite link, `I` invitations waiting for you, `J` join, `t` show in Todos |
| Sync | `p` pull since the cursor, `f` full pull, `c` clear cursor, `enter` open the todo, `d` delete something changed elsewhere |

The command palette is a searchable list of every action with its key. It
runs the key for you, so it still works if you have rebound that key.

### Saved views

A saved view is a name for a combination you use often: a list, a status, a
person, a search, only undated todos, and whether it is shown as a list or a
board. "Work today" might be the Work list, the Today status and todos
assigned to you.

- `s` saves what is on screen under a name. The view is listed under SAVED in
  the sidebar, where `enter` opens it, and in the command palette as
  "Views: open …". `S` opens a menu to open, rename, update (replace what it
  holds with the filters on screen) or remove one. Removing asks first, and
  removes only the view: your todos and lists are not touched.
- `@` picks one person from the members of the list on screen, or of all your
  lists, and `U` narrows to the todos with no date. Both are filters like any
  other, so a view can hold them. A dated status such as Today cannot hold an
  undated todo, so `U` switches to Everything.
- Opening a view applies the whole combination and shows it in the title of
  the list, for example `Work today · Today only · Work · assigned to me`.
  The status is kept as the word, never as a date, so "today" is the day it is
  when you open the view, in your account's timezone.
- A view that names a list you have left, or a person who is no longer a
  member, is not opened with that part left out, which would show more than
  you asked for. It opens as "needs repair", says what is missing, and is
  marked with `!` in the sidebar. `enter` asks what replaces it, or lets you
  drop that part, and saves the result. `esc` leaves it as it was.
- Views are local preferences in `config.json`, kept for each server and
  account, so signing in as someone else shows only that account's views.
  Use without an account has its own set. Sign out keeps them, like your theme
  and keys. Saving a view never writes a token passed with `-token`.

## What it covers

Every endpoint in the public API:

- Todos: filter by status, list, assignee, undated, or free-text search.
  Create, with server-side date parsing and newline splitting for quick
  capture. Edit, complete, reopen, delete and preview how a title parses.
- Checklists: list, add, rename, tick, delete steps.
- Lists: create, rename, delete, members, invite by username or email (they join once they accept),
  remove or leave, invite links, join by code or URL, and an inbox of the
  invitations sent to you (accept, decline, undo a decline). A list made
  offline, or before signing in, shows as `unsynced` until a sync creates it
  in the account; sharing it or showing its members says so rather than
  failing. Account: set or remove your username.
- Sync: the change feed and `/sync`, with a persistent opaque `cursor`,
  tombstones shown as deletions, automatic draining while `hasMore` is
  true, and pushes the server cuts short (`deferred`) sent again.
- Account: profile, timezone, smarter dates, todo import and export, sign out of this computer, and
  account deletion.

Smarter dates is an account setting, off by default and shown only when the
server offers it. Its disclosure explains what goes to the decision model.
While adding a todo, `ctrl+d` reads words kept as a name as a date instead,
and `ctrl+t` flips an assumed morning or evening time. Capture without a
connection keeps using the local date reader.

A time that has already passed today is read by its words. "Call Steven today
at 3pm" typed at 4 PM stays on today at 3 PM, with no reminder, because one
would go off at once. "Call Steven at 3pm" names no day, so it goes to
tomorrow. The line under the title says which happened, and `ctrl+o` switches
to the other reading without changing what you typed. From a script, `evado
parse` says the same and `-passed kept` or `-passed moved` chooses.

An account reads a date written in numbers month first or day first (3/4 is
March 4 or 3 April), as its `dateOrder` says, and the local reader follows the
account. Without an account it reads month first.

Two groups of endpoints cannot be used with a pasted API token, because the
server rejects token auth for them with `403 session_required`:

- token and notification-channel management
- full account export and deletion, which need the `account` scope

The client implements them and shows the real error. The Account tab groups
those sections under WEB APP ONLY and says where to do it instead, which is the
web app. The server enforces this rule, so the client is not missing anything.

### Starting fresh

The Account tab has two ways to leave, and they are not the same.

- Sign out, under THIS COMPUTER, removes the saved token or session, the todos
  kept here and your last view from this computer, and ends a session from an
  emailed code on the server too. Your account and todos on the server
  stay, and so do your settings such as theme, keys and saved views. Changes the server
  has not heard yet are sent first, and the prompt warns you if there are
  any. With no account the todos exist nowhere else, so the prompt says that
  signing out deletes them and asks twice. It works from the palette too:
  press `:` and type "sign out".
- Delete account, under DANGER ZONE, deletes the account on the server for
  every device, then signs this computer out as Sign out does. Use it only
  when you mean that.

## Behaviour worth knowing

- The inbox is always on screen. The Todos tab groups todos into Overdue,
  Today, Inbox, Tomorrow, Later and Done from one fetch of everything. A
  capture with no date is the common case, and a filter that hid it would
  make new work look lost. `f` narrows to one status at a time, and the line
  above the list says so.
- Offline first. The todos live in `todos.json` next to the config file,
  with owner-only permissions like the token file, and the TUI reads and
  writes that. Everything you can do to a todo, a checklist or a date works
  with no connection and with no account. With an account the file syncs in
  the background: at start, two seconds after a change, on every poll (ten
  minutes by default, see `pollSeconds`) and on `r`. The header says where
  things stand: `✓ synced`, `↑ 3` waiting to go, `offline · 3 to send`, or
  `on this computer` when there is no account. After a failed sync the next
  try backs off from 30 seconds up to ten minutes.
- Conflicts are settled field by field. Each field you change here is
  remembered with what the server had before, so when the two meet a field
  changed only here keeps your change, a field changed only elsewhere takes
  that change, and a field changed on both sides goes to the change made
  last. A rename here and a date changed on your phone both survive, even
  when the phone's change came later. The checklist counts as one field. A
  deletion wins over an edit on the other side, wherever it was made. When
  a newer change replaces one of yours, a warning after the sync says
  which, once: `1 edit was replaced by a newer change: 'Pay rent' (title)`.
  If the server refuses a change, for example to a list that became
  read-only, the todo is marked and left alone until you edit it again; the
  rest still sync.
- A large backlog goes up in more than one request. The server applies as
  many changes as fit in one request and hands back the rest as `deferred`
  (the TUI asks for that with `acceptDeferred`). Those are not refused: they
  stay pending and go in the next request of the same sync, in order. If a
  request gets nothing applied at all, the sync stops sending, finishes
  reading what the server has, reports that it will carry on next time, and
  the todos wait for the next sync.
- A list you make while offline is created on the server at the next sync
  with an id this computer chose and saved first. If the answer is lost, or
  the file cannot be saved after it, the next sync asks for that id again
  and gets the same list, not a second one. If the list was deleted
  elsewhere in between, it is not made again: it goes from here as any list
  deleted elsewhere does, below.
- A list you delete while offline is deleted on the server at the next sync,
  after the todos you moved out of it have been moved. If the server turns a
  move down, the list is not deleted: it comes back, with the todo, and the
  warning says why: `deleting "Trip": 'Pay rent' could not be moved out of it`.
- A list deleted elsewhere, or one you were removed from, goes from here at
  the next sync, with its todos. A todo you made here that the server never
  had moves to your personal list instead and is sent there, and a warning
  says so: `2 todos made here were moved to Personal: the list 'Trip' is gone
  from the account`. A todo you moved into the list and never sent goes back
  to the list the server has it in. Unsent edits to a todo the server had are
  lost with the list, and the warning names it: `1 edit was lost with the list
  'Trip', which is gone from the account: 'Pay rent'`.
- More than one copy of evado can use the file at once, such as the TUI and
  an `evado -local` command in another shell. Each takes a lock file,
  `todos.json.lock`, while it reads and writes, and reads the file again if
  another copy saved since it last looked, so neither saves over the
  other's change.
- Signing in with todos already here. If the account is empty they are sent
  up without a word. If both sides have todos you are asked first; yes adds
  this computer's todos to the account and deletes nothing, and no leaves
  this computer as it was. Your local personal list becomes the account's,
  and any other list is matched to one of the same name or created.
- Dates with no server. A small built-in reader handles today, tomorrow,
  weekdays, "next friday", "friday after next", "in 3 days", "a week from
  today", "on the 15th", "end of the month", "oct 3", ISO dates, times such
  as 5pm or 17:30, and repeats such as "every monday" or "daily". It reads
  these the way the service does. A time with no am/pm ("at 3", "half past
  5") is the soonest one between 06:00 and 21:00 (or by the hour, when the
  text names a day), and the New todo form says it was assumed and how to
  type the other half, for example `(assumed; type 3am for morning)`.
  Phrases about the past ("yesterday", "last friday") get no date. The
  service's own parser knows more phrasings. Anything not recognised stays
  in the title and the todo lands in the inbox; the New todo form shows the
  reading as you type.
- What still needs the server: sharing (members, invites, joining, answering
  an invitation; the invitations last synced can be read offline), creating,
  renaming or deleting a list once signed in, the Sync tab, export, and
  changing the account timezone or username, and taking a todo in a shared
  list or handing it back (`i`). With no account these say so rather than
  fail.
- Taking a todo. In a shared list `i` takes the todo under the cursor
  ("I'll do it") while it is Anyone's, or hands yours back. The service
  picks one winner when two people try at once, so this is never saved for
  later: offline `i` says it needs a connection, with no account that it
  needs one, and either way nothing changes. If someone beat you to it, the row is updated from the
  service's answer and the message says who has it. Until then a todo shows
  the assignee from the last sync, or one you set with `A` that is still
  waiting to go up.
- Online only. `-no-cache` or `"noCache": true` keeps nothing on disk: the
  TUI talks straight to the server, as it did before it kept todos locally.
- Cheap refreshes, in online-only mode. Once the whole account is loaded, a
  refresh asks the change feed for what changed instead of fetching
  everything again.
  Completing, reopening, editing or deleting merges the server's answer into
  the list with no further request. A narrowed view, meaning a filter, a
  search or one list, still loads from the server, and `r` always reloads
  everything.
- Actions show at once. Completing or reopening a todo updates the list
  before the server replies. If the server refuses, the list reloads and the
  error says why.
- Undo. `z` takes back the last complete, reopen or delete, up to 20. The
  API cannot restore a deleted todo, so undoing a delete recreates it from
  what was on screen. Title, notes, dates, list and assignee come back, but
  checklist steps and the id do not. Bulk deletes cannot be undone.
- Search as you type. `/` narrows the loaded list on every keystroke,
  `enter` asks the server too, and `esc` clears it. From another tab it takes
  you to Todos first.
- Marking several. `v` marks a todo and steps down. With marks, `space`
  completes them all, `d` deletes them after one confirmation, `m` moves
  them to another list and `esc` clears the marks. The app drops marks on todos
  that leave the screen, so an action never touches something you cannot see.
- The header describes the account, not the view. The open, today and
  overdue counts come from the last unfiltered load and use the same rules
  as the section headings, so they agree with the list and stay put when you
  narrow it.
- Big accounts. A narrowed view loads 200 todos at a time. When more match
  it says so, and `L` loads the next 200 after the ones shown, as many times
  as there are more. A reload after a change keeps the pages already loaded,
  and a new filter starts again from the first page.
- Every action shows its result. After creating, completing, reopening or
  saving, the cursor lands on the todo that changed and a receipt names it.
  If the current view would have hidden the result, the view opens up and
  the receipt says so, for example `added "Renew passport". The overdue only
  view would have hidden it, so it now shows everything`.
- Dates. The server parses natural language in your account timezone, and
  the client formats everything through that zone instead of the machine's,
  so a laptop abroad still shows Today correctly. `p` shows how a title
  parses without creating anything. Date fields in the edit form take words
  such as `friday` or `5pm tomorrow` as well as `YYYY-MM-DD`. If a phrase has
  no date in it, nothing is saved and the message names the field.
- Edits are minimal. The edit form compares against the values it was
  opened with and sends only what changed, so an untouched date field never
  becomes a `null` that wipes it.
- Clearing is explicit. Emptying a date field sends a real JSON `null`,
  which is the documented way to clear it and differs from omitting it.
- Destructive actions take two steps. Deleting a list, leaving a shared
  list, or deleting the account needs two confirmations.
- Read-only lists. When a list owner deletes their account, the list stays
  for its members. The app marks it with a lock icon and calls it frozen, and the client
  refuses rename, delete and new members. Only completing todos and ticking
  steps work.
- Errors. Every response carries `X-Request-Id`, and failures show it so a
  report can be traced. `429` shows the retry window, and a rejected token
  drops you back to the token screen.
- Copying works over SSH. `y` and the invite link use a clipboard program
  when there is one, such as `pbcopy`, `wl-copy`, `xclip`, `xsel` or `clip.exe`,
  and otherwise ask the terminal to copy with an OSC 52 sequence, which most
  modern terminals honour, including through tmux. The text is always shown
  as well.
- Enter saves, with two exceptions. In a form, `enter` commits. In a field
  that collects several lines, such as notes or quick capture, `enter` adds a
  line and `ctrl+s` commits. The hint under the box says so.

## Development

```sh
mise trust
mise run test       # unit and integration tests
mise run check      # gofumpt + go vet + golangci-lint
mise run build      # host binary, stamped with the git version
mise run build-all  # every release target into dist/
```

[docs/releasing.md](docs/releasing.md) covers tagging a release and updating
Homebrew and Nix.

Layout:

```
cmd/evado         the binary: flags, config, wiring, the CLI commands
cmd/fakeserver    a stand-in API for driving the TUI by hand
internal/api      typed client for the whole public API
internal/auth     the session an emailed code starts, and renewing it
internal/config   token and preference storage
internal/dates    date parsing shared by the TUI forms and the CLI
internal/local    the todos kept on this computer, and syncing them
internal/ops      account operations behind the CLI
internal/testapi  an in-memory API for tests
internal/tui      the interface: app shell, views, forms, overlays
```

The Windows builds carry the Evado icon and file properties. They come from
`cmd/evado/rsrc_windows_*.syso`, which `go build` links in on its own, so
nothing extra runs at build time. `assets/evado.svg` is the source. To change
the icon, replace it and regenerate the resources with `rsvg-convert` and
[go-winres](https://github.com/tc-hib/go-winres):

```sh
for s in 16 32 48 256; do
  rsvg-convert -w $s -h $s assets/evado.svg -o cmd/evado/winres/icon$s.png
done
(cd cmd/evado && go-winres make --arch amd64,arm64)
```

There is a fake server so the UI can be exercised without an account:

```sh
mise x -- go run ./cmd/fakeserver -addr 127.0.0.1:8787 &
EVADO_CONFIG=/tmp/evado-dev.json mise x -- go run ./cmd/evado \
  -server http://127.0.0.1:8787/api/v1 -token evt_dev
```

It answers only requests that carry its token, `evt_dev` unless you start it
with `-token`, and it listens only on a loopback address unless you add
`-remote`. It handles one request at a time.

The integration tests drive the real Bubble Tea model against a fake API
through a fake terminal, asserting on rendered output: `internal/tui`
covers navigation, forms, confirmations and error handling;
`internal/api` covers request shapes, error mapping and rate limits.

## License

MIT. See [LICENSE](LICENSE). Release archives include the notices for the
libraries Evado is built with.

## Portability

`CGO_ENABLED=0` and no build tags: the binary has no libc dependency, so it
runs on any Linux, on macOS, and on Windows without a runtime. Text
measurement goes through `lipgloss`, which handles double-width characters
and combining marks, so CJK and emoji titles do not break the layout.
