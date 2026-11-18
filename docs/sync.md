# Offline first, and how sync works

Evado keeps your todos in a file on this computer and works from that. With an
account, the file syncs with the service in the background. This page says
what happens in each case, including the awkward ones.

## The file on this computer

### Offline first

The todos live in `todos.json` next to the config file, with owner-only
permissions like the token file, and the TUI reads and writes that. Everything
you can do to a todo, a checklist or a date works with no connection and with
no account. With an account the file syncs in the background: at start, two
seconds after a change, on every poll (ten minutes by default, see
`pollSeconds` in [configuration](configuration.md)) and on `r`. The header
says where things stand: `✓ synced`, `↑ 3` waiting to go, `offline · 3 to
send`, or `on this computer` when there is no account. After a failed sync the
next try backs off from 30 seconds up to ten minutes.

### More than one copy of evado at once

More than one copy of evado can use the file at once, such as the TUI and an
`evado -local` command in another shell. Each takes a lock file,
`todos.json.lock`, while it reads and writes, and reads the file again if
another copy saved since it last looked, so neither saves over the other's
change.

### Signing in with todos already here

If the account is empty, the todos here are sent up without a word. If both
sides have todos you are asked first; yes adds this computer's todos to the
account and deletes nothing, and no leaves this computer as it was. Your local
personal list becomes the account's, and any other list is matched to one of
the same name or created.

## When two sides changed the same thing

### Conflicts are settled field by field

Each field you change here is remembered with what the server had before, so
when the two meet a field changed only here keeps your change, a field changed
only elsewhere takes that change, and a field changed on both sides goes to
the change made last. A rename here and a date changed on your phone both
survive, even when the phone's change came later. The checklist counts as one
field. A deletion wins over an edit on the other side, wherever it was made.
When a newer change replaces one of yours, a warning after the sync says
which, once: `1 edit was replaced by a newer change: 'Pay rent' (title)`. If
the server refuses a change, for example to a list that became read-only, the
todo is marked and left alone until you edit it again; the rest still sync.

### A large backlog goes up in more than one request

The server applies as many changes as fit in one request and hands back the
rest as `deferred` (the TUI asks for that with `acceptDeferred`). Those are
not refused: they stay pending and go in the next request of the same sync, in
order. If a request gets nothing applied at all, the sync stops sending,
finishes reading what the server has, reports that it will carry on next time,
and the todos wait for the next sync.

## Lists made or deleted while offline

### A list you make while offline

A list you make while offline is created on the server at the next sync with
an id this computer chose and saved first. If the answer is lost, or the file
cannot be saved after it, the next sync asks for that id again and gets the
same list, not a second one. If the list was deleted elsewhere in between, it
is not made again: it goes from here as any list deleted elsewhere does,
below.

### A list you delete while offline

A list you delete while offline is deleted on the server at the next sync,
after the todos you moved out of it have been moved. If the server turns a
move down, the list is not deleted: it comes back, with the todo, and the
warning says why: `deleting "Trip": 'Pay rent' could not be moved out of it`.

### A list deleted elsewhere, or one you were removed from

A list deleted elsewhere, or one you were removed from, goes from here at the
next sync, with its todos. A todo you made here that the server never had
moves to your personal list instead and is sent there, and a warning says so:
`2 todos made here were moved to Personal: the list 'Trip' is gone from the
account`. A todo you moved into the list and never sent goes back to the list
the server has it in. Unsent edits to a todo the server had are lost with the
list, and the warning names it: `1 edit was lost with the list 'Trip', which
is gone from the account: 'Pay rent'`.

## What needs a connection

### What still needs the server

Sharing (members, invites, joining, answering an invitation; the invitations
last synced can be read offline), creating, renaming or deleting a list once
signed in, the Sync tab, export, and changing the account timezone or
username, and taking a todo in a shared list or handing it back (`i`). With no
account these say so rather than fail.

### Online only

`-no-cache` or `"noCache": true` keeps nothing on disk: the TUI talks straight
to the server, as it did before it kept todos locally.

### Cheap refreshes in online-only mode

Once the whole account is loaded, a refresh asks the change feed for what
changed instead of fetching everything again. Completing, reopening, editing
or deleting merges the server's answer into the list with no further request.
A narrowed view, meaning a filter, a search or one list, still loads from the
server, and `r` always reloads everything.

## What the client covers

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

Two groups of endpoints cannot be used with a pasted API token, because the
server rejects token auth for them with `403 session_required`:

- token and notification-channel management
- full account export and deletion, which need the `account` scope

The client implements them and shows the real error. The Account tab groups
those sections under WEB APP ONLY and says where to do it instead, which is
the web app. The server enforces this rule, so the client is not missing
anything.