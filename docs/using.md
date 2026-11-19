# Using the app

Press `?` inside the app for every key on the screen you are on. This page is
the longer version: the keys, saved views, how dates are read, and the details
of how the app behaves.

## Keys

### Everywhere

| key | does |
| --- | --- |
| `1` to `4` | switch tab |
| `tab` | next tab |
| `r` | refresh |
| `/` | search |
| `:` or `ctrl+p` | command palette |
| `ctrl+t` | choose a theme |
| `?` | help |
| `q` | quit |

The command palette is a searchable list of every action with its key. It runs
the key for you, so it still works if you have rebound that key.

### Todos

Everyday:

| key | does |
| --- | --- |
| `j` / `k` | move down or up |
| `enter` | open the todo |
| `space` | complete it, or reopen it |
| `n` | new todo |
| `N` | quick capture, one todo per line |
| `e` | edit |
| `d` | delete |
| `z` | undo |

Rescheduling:

| key | does |
| --- | --- |
| `t` / `T` / `w` | move to today, tomorrow or next week |
| `<` / `>` | move a day earlier or later |

Narrowing what is shown:

| key | does |
| --- | --- |
| `/` | search |
| `f` | cycle the view: everything, overdue, today and so on |
| `F` | pick a list |
| `[` / `]` | previous or next list |
| `a` | cycle the assignee: everyone, me, unassigned |
| `@` | pick one person |
| `U` | only undated todos |
| `u` | jump to the inbox |
| `L` | load more |
| `s` | save the filters as a view |
| `S` | open, rename, update or remove a view |

Several at once, layout and the rest:

| key | does |
| --- | --- |
| `v` | mark a todo |
| `m` | move the marked todos |
| `b` | board, a column per day |
| `h` | sidebar |
| `\` | hide the sidebar |
| `p` | preview how a title's date is read |
| `y` / `Y` | copy the title or the id |
| `i` | in a shared list, take the todo or hand yours back to Anyone |

### Forms

| key | does |
| --- | --- |
| `enter` | save |
| `ctrl+s` | save from a notes box |
| `ctrl+e` | open a notes box in `$VISUAL` or `$EDITOR` |
| `esc` | cancel |

### An open todo

| key | does |
| --- | --- |
| `tab` | go to the checklist |
| `c` | add a step |
| `space` | tick a step |
| `e` | rename a step |
| `d` | delete a step |
| `A` | assign |
| `i` | take the todo or hand it back (shared lists) |
| `y` | copy the id |

### Lists tab

| key | does |
| --- | --- |
| `n` | new list |
| `e` | rename |
| `d` | delete |
| `m` | show members |
| `M` | manage members |
| `a` | invite by username or email, or pick someone you share a list with |
| `i` | make a 7-day invite link |
| `I` | invitations waiting for you |
| `J` | join |
| `t` | show the list in Todos |

### Sync tab

| key | does |
| --- | --- |
| `p` | pull since the cursor |
| `f` | full pull |
| `c` | clear the cursor |
| `enter` | open the todo |
| `d` | delete something changed elsewhere |

Keys can be remapped in the config file; see
[configuration](configuration.md).

## Saved views

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

## Dates

### Dates are read in your account timezone

The server parses natural language in your account timezone, and the client
formats everything through that zone instead of the machine's, so a laptop
abroad still shows Today correctly. `p` shows how a title parses without
creating anything. Date fields in the edit form take words such as `friday` or
`5pm tomorrow` as well as `YYYY-MM-DD`. If a phrase has no date in it, nothing
is saved and the message names the field.

### Dates with no server

A small built-in reader handles today, tomorrow, weekdays, "next friday",
"friday after next", "in 3 days", "a week from today", "on the 15th", "end of
the month", "oct 3", ISO dates, times such as 5pm or 17:30, and repeats such
as "every monday" or "daily". It reads these the way the service does. A time
with no am/pm ("at 3", "half past 5") is the soonest one between 06:00 and
21:00 (or by the hour, when the text names a day), and the New todo form says
it was assumed and how to type the other half, for example `(assumed; type 3am
for morning)`. Phrases about the past ("yesterday", "last friday") get no
date. The service's own parser knows more phrasings. Anything not recognised
stays in the title and the todo lands in the inbox; the New todo form shows
the reading as you type.

### Names, guessed times and times that have passed

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
account. Without an account it follows the computer's time zone the way the
service does for an account that has not chosen: month first in the United
States, Canada, the Philippines, China, Taiwan, Japan and Korea, and day first
everywhere else. Set `dateOrder` in `config.json` to pick one yourself; see
[configuration](configuration.md#the-config-file). Dates written in words read
the same either way.

## The Todos tab

The Todos tab has a sidebar of views and lists with live counts, this month at
a glance, and what you finished over the last two weeks. On a wide terminal
the todo under the cursor opens alongside the list. `b` turns the list into a
board with a column per day, where `<` and `>` move a todo from one day to the
next. `ctrl+t` picks a theme.

### The inbox is always on screen

The Todos tab groups todos into Overdue, Today, Inbox, Tomorrow, Later and
Done from one fetch of everything. A capture with no date is the common case,
and a filter that hid it would make new work look lost. `f` narrows to one
status at a time, and the line above the list says so.

### The header describes the account, not the view

The open, today and overdue counts come from the last unfiltered load and use
the same rules as the section headings, so they agree with the list and stay
put when you narrow it.

### Search as you type

`/` narrows the loaded list on every keystroke, `enter` asks the server too,
and `esc` clears it. From another tab it takes you to Todos first.

### Marking several

`v` marks a todo and steps down. With marks, `space` completes them all, `d`
deletes them after one confirmation, `m` moves them to another list and `esc`
clears the marks. The app drops marks on todos that leave the screen, so an
action never touches something you cannot see.

### Big accounts

A narrowed view loads 200 todos at a time. When more match it says so, and `L`
loads the next 200 after the ones shown, as many times as there are more. A
reload after a change keeps the pages already loaded, and a new filter starts
again from the first page.

## Changing todos

### Actions show at once

Completing or reopening a todo updates the list before the server replies. If
the server refuses, the list reloads and the error says why.

### Every action shows its result

After creating, completing, reopening or saving, the cursor lands on the todo
that changed and a receipt names it. If the current view would have hidden the
result, the view opens up and the receipt says so, for example `added "Renew
passport". The overdue only view would have hidden it, so it now shows
everything`.

### Undo

`z` takes back the last complete, reopen or delete, up to 20. The API cannot
restore a deleted todo, so undoing a delete recreates it from what was on
screen. Title, notes, dates, list and assignee come back, but checklist steps
and the id do not. Bulk deletes cannot be undone.

### Edits are minimal

The edit form compares against the values it was opened with and sends only
what changed, so an untouched date field never becomes a `null` that wipes it.

### Clearing is explicit

Emptying a date field sends a real JSON `null`, which is the documented way to
clear it and differs from omitting it.

### Destructive actions take two steps

Deleting a list, leaving a shared list, or deleting the account needs two
confirmations.

### Enter saves, with two exceptions

In a form, `enter` commits. In a field that collects several lines, such as
notes or quick capture, `enter` adds a line and `ctrl+s` commits. The hint
under the box says so.

## Shared lists

### Taking a todo

In a shared list `i` takes the todo under the cursor ("I'll do it") while it
is Anyone's, or hands yours back. The service picks one winner when two people
try at once, so this is never saved for later: offline `i` says it needs a
connection, with no account that it needs one, and either way nothing changes.
If someone beat you to it, the row is updated from the service's answer and
the message says who has it. Until then a todo shows the assignee from the
last sync, or one you set with `A` that is still waiting to go up.

### Read-only lists

When a list owner deletes their account, the list stays for its members. The
app marks it with a lock icon and calls it frozen, and the client refuses
rename, delete and new members. Only completing todos and ticking steps work.

## Errors and copying

### Errors

Every response carries `X-Request-Id`, and failures show it so a report can be
traced. `429` shows the retry window, and a rejected token drops you back to
the token screen.

### Copying works over SSH

`y` and the invite link use a clipboard program when there is one, such as
`pbcopy`, `wl-copy`, `xclip`, `xsel` or `clip.exe`, and otherwise ask the
terminal to copy with an OSC 52 sequence, which most modern terminals honour,
including through tmux. The text is always shown as well.
