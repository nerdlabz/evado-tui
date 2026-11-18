# Evado for agents

There are two ways for an AI agent to read and change an Evado account without
the terminal UI: the service's own MCP endpoint, and this repo's CLI. Use MCP
when the agent runtime supports it. Use the CLI when the agent can only run
shell commands, or from scripts and cron.

## Setup

Get a token from Settings, API & apps in your Evado account. It starts with
`evt_`. Give it to the process through the environment, which writes nothing
to disk.

```sh
export EVADO_TOKEN=evt_…
export EVADO_SERVER=https://your-host/api/v1   # only to test against another server
```

A flag beats the environment, and the environment beats the saved config file.
A config file that is there but cannot be read stops every command with exit 1
and an error naming the file, even when the token comes from a flag or the
environment, so a script never runs on settings other than the ones saved.
`-save` will not write over it either: fix it or move it aside.

With no token the CLI fails with exit code 3, unless it is asked to work on the
todos kept on this computer instead (see [The todos on this
computer](#the-todos-on-this-computer)).

## MCP

The Evado service serves MCP itself, so there is nothing to install. It is
streamable HTTP at `https://<host>/mcp`, authenticated with the same token.

```sh
claude mcp add --transport http evado https://<host>/mcp --header "Authorization: Bearer evt_…"
```

The service documents its own tools. This repo has no MCP server.

## CLI

```sh
evado ls -status today -json
evado ls -status all -limit 1000 -json       # past 200, read a page at a time
evado ls -status all -cursor "$NEXT" -json   # carry on where an earlier ls stopped
evado add -list Work "Send the report Friday 3pm"
printf 'milk\neggs\n' | evado add -          # one todo per line
evado show 3fa9 -json
evado edit 3fa9 -day tomorrow -assign sam@example.com
evado edit 3fa9 -due none                    # clear a field
evado claim 3fa9                             # take it, only if nobody has (shared lists)
evado release 3fa9                           # hand yours back to Anyone
evado done 3fa9 8b21                         # several at once
evado rm -yes 3fa9                           # nothing is deleted without -yes
evado steps list 3fa9                        # a todo's checklist, with step ids
evado steps add 3fa9 "Call the bank"         # same as: evado step 3fa9 "Call the bank"
evado steps done 3fa9 7c1e                   # sets the state; repeating it changes nothing
evado steps edit 3fa9 7c1e "Call the bank again"
evado steps rm -yes 3fa9 7c1e
evado changes -json                          # every live todo, and a first cursor
evado changes -since 40412 -json             # what changed after that cursor, deletes included
evado export todos.json                      # portable lists, todos and checklists
evado import -dry-run todos.json             # preview without writing
evado import todos.json                      # bring in what is not already here
evado -local import todos.json               # the same, on this computer
```

The commands are `add`, `ls`, `show`, `done`, `undone`, `edit`, `claim`,
`release`, `rm`, `step`, `steps`, `changes`, `lists`, `parse`, `export` and `import`. Global flags such as `-server` and
`-token` go before the command. Every command takes `-json`, and `EVADO_JSON=1` makes that
the default. Other flags can come before or after the arguments.

`evado help` prints the usage, and `evado help <command>` or `evado <command>
-h` prints one command's synopsis and flags. Help goes to standard output,
exits 0, and needs no token.

`add -` takes any number of lines. The service reads at most 2000 characters
and 20 lines at a time, so longer input goes up in batches, in order. If a
batch fails, the todos already added are printed before the error, so re-run
only the rest.

### Import and export

`evado export [file | -]` writes a portable Evado JSON file. With no path, or
with `-`, it writes to standard output. A named file uses `0600` permissions on
Unix. The file includes lists, todos, notes and checklists, completed todos
included, and leaves out deleted todos, assignees, members and account records.
It uses `"format": "evado-export", "version": 1`.

`evado import [-dry-run] [-list <id>] [-json] <file | ->` reads that file, a full
Evado account export, or a legacy Evado export. `-dry-run` reports what would
happen without writing. `-list` selects one writable destination list;
otherwise source lists are kept as private copies or matched to lists already
here. Existing todo ids are skipped, even if the file has a newer timestamp,
and deleted todos stay deleted. Sharing is ignored. Only future reminders on
open todos are kept; no default reminder is created.

Use `-local` for the todos on this computer. Without it, the commands reach
the server and need the ordinary `todos.read` or `todos.write` scope. Files
from other todo apps belong in the web app's import screen.

The import report counts added, skipped and refused todos and fields that
could not be kept. The server reads at most 25,000 todos in a 32 MiB file and
adds at most 10,000 new todos per request, also bounded by checklist rows. If
the report has `complete: false`, the command prints it and exits 10. Run the
same file again to continue; existing ids make that safe. Neither a partial
response nor an error starts an automatic retry.

### Ids

A todo is a full id or the start of one. If the start matches more than one
todo, the error lists the candidates. `ls` prints the first eight characters,
and `-json` or `-full-ids` gives the whole id.

A short id is checked against every todo in the account, which takes a few
requests on a large account. If the account cannot be listed to the end, the
short id is refused with exit code 2 and a full id is needed.

A checklist step is named by its todo and its own id: `steps done <todo>
<step>`. The step id may be shortened to a prefix that is unique within that
todo's checklist, and an ambiguous one is refused with the candidates listed
(exit 2). A step id from another todo is not found (exit 4); the step is always
looked up in the checklist of the todo you named. `steps list` prints short
ids, or whole ones with `-full-ids`, and `-json` always has whole ids. On a
read-only shared list the service allows `steps done` and `steps undone` but
refuses `add`, `edit` and `rm` (exit 6).

### Listing more than 200 todos

`ls` returns at most `-limit` todos, 200 when `-limit` is left out. The
service gives them in pages of 200, so a larger `-limit` is read one page at
a time, in order, until the limit is reached or nothing more matches. If any
page fails the command prints nothing and exits with that error's code, so a
short list is never mistaken for a whole one.

When more todos match than were returned, `ls` still exits 0, so a script that
worked before keeps working. In text, a line on standard error says how many
were shown and gives the cursor. With `-json` the output keeps its `count` and
`todos` and adds `nextCursor`: the cursor when more match, `null` when this is
the end. To carry on, pass it back as `-cursor`, with the same `-status`,
`-list`, `-q` and `-assigned`:

```sh
evado ls -status all -limit 500 -json > page1.json
evado ls -status all -limit 500 -cursor "$(jq -r .nextCursor page1.json)" -json
```

A cursor is the service's own position in the list, not an id and not a time:
do not build or edit one. It only means something with the filters it came
with, and `-cursor` with a malformed one is refused (exit 6). It is not the
cursor `changes` prints; the two do not mix. A todo whose date changes while
you read can be seen twice or missed, so for a copy that has to stay exact use
`changes`. `-local` pages the same way.

### Claiming a todo

In a shared list, `claim` assigns a todo to you only while it is Anyone's
(unassigned, or assigned to someone who left the list), and `release` hands
yours back to Anyone only while it is still yours. Use them instead of
`edit -assign`, which overwrites whoever took the todo a moment earlier.
The service decides between two people claiming at once: one wins, and the
other gets exit 6 with code `already_claimed`. A release after someone
reassigned the todo changes nothing and gets `not_assigned_to_you`, and
either one on a done todo gets `todo_completed`. With `-json` the error on
stderr also has `todo`, the todo as it is now (absent for `todo_completed`),
and `assignee`, the member who has it (absent when that is someone who left
the list). A todo in your personal list is refused with `personal_list`, and
one in a read-only list with `list_read_only`, both exit 6.

Repeating a claim or release that already worked is safe: it returns the
todo unchanged, with `"changed": false` in the JSON.

### Changes since a cursor

`evado changes -since <cursor> -json` reads the service's change feed: every
todo that changed after that cursor, in every list you can see, in the order
the service gives them. It is for cron jobs and agents that want what is new
without listing everything again. A cursor is the `cursor` an earlier run
printed, passed back exactly as it was. It is a position in the service's
change log, not a time: do not build one, compare two, or move it back "to be
safe", and a time such as `2026-03-10T15:00:00Z` is refused before any request.
Leave `-since` out on the first run: that reads every live todo and gives you
the first cursor. `-status`, `-list`, `-q`, `-assigned` and `-limit` are refused
(exit 2), because the feed ignores filters and would return everything.

This is an incremental sync feed, not an audit log and not an exactly-once
stream. It says what a todo looks like now, not every step it took to get
there. A todo that changed twice since your time may appear more than once, so
keep the last entry for each id, and a change can show up in two runs in a row,
so apply entries idempotently. A write that lands while a run is reading is
never lost: it arrives in a later run. Nothing is stored
between runs: you keep the cursor.

The JSON has `todos` (full ids, each with its `checklist` inline and
`"deleted": true` for a deleted todo, which is a tombstone to apply, not a live
todo), `lists` and `invitations` (snapshots of the state at the end, not
changes; `[]` means none and `null` means the service sent no snapshot), the
`since` you gave (empty on a first run), `pages`, `complete`, `hasMore`, and
`cursor`. After a run with `complete: true`, save `cursor` and pass it as the
next `-since`. Neither this computer's clock nor the service's plays any part.
The text output is only a summary of counts and the cursor.

The command reads at most `-max-pages` pages of up to 200 todos, 50 pages by
default. If the feed goes on past that, it still prints what it read, with
`complete: false`, `hasMore: true` and `cursor` set to where to resume, and
exits 10. Exit 10 is not a failure: use what was printed, then run again with
`-since` set to that `cursor` (the error on standard error repeats it). It has its own code so that a script can tell it
from an error, which has no output and never says `complete`. If any page
fails (rate limit, server error, network), the command prints no result and
exits with that error's code, so start again from the `-since` you had.

### The todos on this computer

The TUI keeps its todos in `todos.json` next to the config file, and works with
no account. The commands can work on that file instead of the server:

```sh
evado -local add "Water the plants friday"
evado -local ls -json
EVADO_LOCAL=1 evado done 3fa9                # EVADO_LOCAL=1 is -local for every command
```

Which one a command uses is decided before it runs, and nothing falls back from
one to the other:

| what is given | where the command goes |
| --- | --- |
| `-local` or `-local=true` | this computer, even with a token |
| `-local=false` | the server, as below without `EVADO_LOCAL` |
| `EVADO_LOCAL=1` (or `true`), no flag | this computer, even with a token |
| `EVADO_LOCAL=0` (or anything else), no flag | the server, as below |
| a token (`-token`, `EVADO_TOKEN`, config file) | the server, exactly as before |
| no token, and the TUI was set up to use no account | this computer |
| no token otherwise | nothing: the no-token error, exit 3 |

So a script given a token always reaches the account, and one whose token went
missing fails instead of quietly writing to a file.

On this computer a command never touches the network and needs no token. If the
todos here are signed in to an account, a change waits in the file and goes up
the next time the TUI syncs, as a change made offline does; a running TUI
notices it within a few seconds, shows it, and syncs it. Until then the server,
and a command run with a token, do not have it. The other way round, the file
holds what the TUI's last sync brought, so a change made on another device or
with a token shows here only after the TUI next syncs.

| command | on this computer |
| --- | --- |
| `add`, `ls`, `show`, `done`, `undone`, `edit`, `rm` | yes, with the same flags, output and exit codes |
| `step`, `steps list/add/done/undone/edit/rm` | yes |
| `lists`, `parse` | yes; `parse` reads dates the way the TUI does offline |
| `export`, `import` | yes; the same portable Evado file and preservation rules, with no network calls |
| `edit -assign` | yes, for members the last sync saw; a list only here has just you (exit 4 for anyone else) |
| `changes` | no: it is the service's change feed. Refused before anything is read, exit 2, code `needs_server` |
| `claim`, `release` | no, even when the todos here are signed in: the service decides who a shared todo belongs to. Refused the same way, exit 2, `needs_server` |

A title here is cut the way the service stores it: trimmed, then at most 500
UTF-16 code units (an emoji is two), with a character that would be cut in half
dropped whole. That goes for a todo's title and for a step's, when it is made
and when it is edited, so that it comes back from the next sync as it went up.

Short ids work as above; every todo here is read at once, so there is never
an incomplete lookup.

The TUI and a command can use the file at the same time. Each takes the lock
file `todos.json.lock` beside it for as long as one read or change takes,
reads `todos.json` again if another copy of evado saved it since, and saves
before letting go, through a temporary file so the file is always whole. So
neither saves over the other, a change made from either side keeps the other's
changes to other todos and other fields, and the TUI shows a command's change
without a restart. A command waits up to ten seconds for the file. If another
copy holds it longer, or the file is damaged or from a newer evado, nothing is
saved and the command fails with exit 1, or 8 if that happens part way
through a command that had already read the file.

### Dates

The date flags `-day`, `-at`, `-due` and `-remind` accept an ISO date or time
such as `2026-03-10` or `2026-03-10T15:00`, or words the server reads, such as
`friday` or `5pm tomorrow`. The server reads words in the account timezone.
With `-local` they are read by the reader built into this program, which knows
fewer phrasings than the server; what a date means is the same either way.

A day alone, in digits or in words, means the same for every flag:

| flag | `2026-03-10` or `friday` | `friday 5pm` |
| --- | --- | --- |
| `-day` | that day | that day (the time is dropped) |
| `-at` | a do date on that day, with no time of day | that time |
| `-due` | midnight that day, on the account's clock | that time |
| `-remind` | midnight that day, on the account's clock | that time |

If the words hold no usable date, or hold a repeat, which goes in `-repeat`
instead, nothing is saved and the error names the field.

A time with a UTC offset, such as `2026-03-10T15:00:00Z` or
`2026-03-10T15:00:00-05:00`, is that instant. Any other time, in words or in
digits such as `2026-03-10T15:00`, is on the account's clock, as is the
midnight a day alone means for `-due` and `-remind`. `-day` keeps the date as
written, except from a time with an offset: then it is the day that instant
falls on in the account timezone, the day `-at` would put the same value on.

So reading a date usually needs the account timezone. Only a time with an
offset for `-at`, `-due` or `-remind`, and a date with no offset for `-day`,
never need it. If it cannot be loaded, nothing is saved and the error says so,
with the exit code of the failed request (for example 8 for a server error),
or 2 if the timezone is not one this computer knows.

### Exit codes

| code | meaning |
| --- | --- |
| 0 | ok |
| 1 | unexpected failure, or a config file that cannot be read |
| 2 | usage: bad arguments, an unknown command, an ambiguous id, or a delete without `-yes` |
| 3 | no token, or it was rejected |
| 4 | no such todo or list |
| 5 | rate limited; `retryAfterSeconds` says when to try again |
| 6 | the server refused the change: invalid input, a conflict (someone else has the todo), a read-only list |
| 7 | the token may not do this (some actions need a browser session) |
| 8 | server error; retrying may help |
| 9 | could not reach the server |
| 10 | `changes` hit its page bound, or `import` has more to read; the printed result says how to resume |

With `-local` the same codes mean the same things for the todos on this
computer: 2 includes `changes`, `claim` and `release` (`needs_server`), 4 is a todo,
step, list or assignee that is not here, 6 is a change the file's rules
refuse, and 1 or 8 is the file being held by another copy of evado for too
long, or unreadable. 3, 5 and 9 do not happen.

With `-json` or `EVADO_JSON=1`, an error is printed on stderr as
`{"error": {"code", "message", "exitCode", …}}`. Otherwise it is one line
starting with `evado:`.

## Safe habits

- List before you write, and act on ids you were given, not ones you guessed.
- Deleting is permanent and reaches every device, so ask the person first.
- When a call is rate limited or the server fails, wait for `retryAfterSeconds`
  instead of retrying in a loop.
- `add` reads dates out of the text. If the date matters, check it with
  `evado parse` first, or set it explicitly with `evado edit`. A time typed
  without am/pm ("at 3", "half past 5") is a guess at the half of the day, the
  soonest one between 06:00 and 21:00, or by the hour when the text names a
  day. `evado parse` says so, and `-json` has `assumedTime` with the words
  read and what the other half would be (`null` when nothing was guessed);
  typing "3pm" settles it. This is the same on this computer and on the
  server.

## Working on this repo

```sh
mise trust
mise run test       # unit and integration tests
mise run check      # gofumpt + go vet + golangci-lint
mise run build      # dist/evado
```

- `internal/ops` holds the account operations. Add new behaviour there, then
  expose it from `cmd/evado/cli.go`.
- `internal/local` keeps the TUI's todos on this computer and syncs them. It
  answers the same API the service does, so the TUI talks to it through the
  ordinary client. The CLI uses it with `-local`, with no link to the
  service, and talks to the server directly otherwise. `disk.go` there is
  how several processes share the file.
- `internal/auth` keeps the session that signing in to the TUI with an emailed
  code starts, and renews its rotating tokens through an HTTP transport. The
  CLI and pasted API tokens never go through it.
- `internal/testapi` is an in-memory Evado for tests that need a server.
- `internal/tui` is the interface. Its tests drive the real model against a
  fake API.
- Comments say why, not what, and match the surrounding code.
