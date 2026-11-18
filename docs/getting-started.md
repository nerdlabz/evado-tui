# Getting started

## Try it without setting anything up

Run `evado -demo` to try sample todos offline. It uses a temporary profile,
ignores saved and environment credentials, and removes its changes when you
quit. Demo mode is for the TUI only; commands use `-local` or an account.

## Use it without an account

You can start without an account. Run `evado`, choose "Use without an account"
on the first screen, and your todos are kept in a file on this computer. When
you later sign in from Account, Connection, they are added to your account and
kept in sync from then on.

## Sign in

The first screen lists the ways to start. To sign in straight away, press
enter on the first one. Evado opens your browser, where you can sign in or
create an account and approve Evado TUI. The terminal shows a clickable link
if the browser cannot open. Escape cancels; an unanswered sign-in expires
after five minutes. Browser sign-in also allows account deletion from the
Account tab. Exporting todos needs only permission to read todos.

On a machine without a browser, choose "Use an email code or an API token" and
type your existing account email for a six-digit code, or paste an API token;
typing or pasting on the list opens that field too. `ctrl+b` starts browser
sign-in from anywhere on the screen. The server's address is shown only when
it was set to another one, as with `-server`. The session renews itself and is
kept in `session.json` next to the config, with `0600` permissions.

You can also paste an API token from Settings, API & apps in your Evado
account. It starts with `evt_`, and scripts and the CLI commands use it.

`evado -check` verifies the saved token against the API and exits, which is
useful in a script or a container health check.

## Signing out and deleting your account

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

## Next

- [Using the app](using.md): keys, saved views and how dates are read.
- [Offline first, and how sync works](sync.md).
- [Import and export](import-export.md).
