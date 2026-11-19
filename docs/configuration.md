# Configuration

Evado needs no configuration. These are the flags, the config file and the
environment variables for when you want to change something.

## Flags

| flag | meaning |
| --- | --- |
| `-token` | use this token for this run |
| `-save` | save the token from `-token` to the config file |
| `-server` | point at a different API root, such as a test server |
| `-check` | verify the token and print the account, then exit |
| `-poll` | seconds between background refreshes, at least 120, default 600 |
| `-mouse` | turn on the mouse: click, double click and scroll |
| `-no-cache` | keep no todos on this computer: talk straight to the server, online only |
| `-local` | commands only: use the todos kept on this computer instead of the server |
| `-theme` | `auto`, `light`, `dark`, or a theme name such as `tokyonight` |
| `-version` | print the version |

Passing `-token` is for that run only: remembering your last view never writes
it to the config file.

A token can be tried for one run or saved:

```sh
evado -token evt_…          # use it for this run only
evado -save -token evt_…    # save it to the config file
evado -check                # verify the saved token and exit
```

Commands such as `add` and `ls` have flags of their own; `evado help` lists
them, and [AGENTS.md](../AGENTS.md) describes them.

## The config file

`config.json` lives in `~/.config/evado` (under `%AppData%\evado` on Windows).
It holds the token, what the app remembers between runs, and these optional
settings:

```json
{
  "pollSeconds": 600,
  "theme": "auto",
  "mouse": false,
  "noCache": false,
  "dateOrder": "dmy",
  "keys": { "J": "j", "K": "k" }
}
```

- `pollSeconds` sets how often the open view refreshes itself in the
  background. The default is ten minutes, and anything below 120 seconds is
  raised to 120. The app skips a refresh while a form or dialog is open or a
  request is running, and a refresh never shows the busy indicator.
- `noCache` keeps no todos on this computer. The TUI then talks straight to
  the server and needs a connection for everything; see [sync](sync.md).
- `dateOrder` is `mdy` or `dmy`: how a date written in numbers reads without
  an account, so `3/4` is March 4 or 3 April. Left out, it follows the
  computer's time zone: month first in the United States, Canada, the
  Philippines, China, Taiwan, Japan and Korea, day first everywhere else. An
  account reads dates by its own setting instead, and any other value is
  ignored.
- `local` records that you chose to use the TUI without an account.
- `theme` is `auto` to follow the terminal, `light` or `dark` to force the
  house palette, or a built-in theme: `evado`, `evado-light`, `catppuccin`,
  `tokyonight`, `gruvbox`, `nord`, `dracula` or `rosepine`. `ctrl+t` inside
  the app previews each one and saves the one you keep.
- `board` and `hideSidebar` remember whether the Todos tab was left as a
  board and with its sidebar folded away.
- `savedViews` holds the views you saved, in one set for each server and
  account, and one for use without an account. See [saved views](using.md#saved-views).
- `mouse` turns on the mouse. A click selects whatever it lands on: a tab,
  a sidebar entry, a todo, a board card, a step, a list, a change, an
  account section, or a field or option in a dialog. A double click opens a
  todo, shows a list's todos, or runs an account section. A click on a
  circle or a step's box completes it, a click on a key hint in the footer
  presses that key, and the wheel scrolls whatever is under the pointer. A
  click beside the command palette, the theme picker or the help closes it.
  It is off by default because capturing the mouse stops the terminal
  selecting text; most terminals still select while Shift (Option in
  iTerm2 and Terminal.app) is held. Account, Mouse, or "Turn the mouse on or off" in
  the command palette, switches it without a restart and saves the choice.
- `keys` remaps keys in the main views: pressing the key on the left does
  what the key on the right does. Names are single characters, `space`,
  `enter`, `esc`, `tab`, arrows, `pgup`/`pgdown`, `home`/`end` and
  `ctrl+a` to `ctrl+z`. Text entry is never remapped.

## Environment

| variable | effect |
| --- | --- |
| `EVADO_CONFIG` | config file path |
| `XDG_CONFIG_HOME` | config directory (default `~/.config/evado`) |
| `EVADO_DATA_DIR` | where account exports are written |
| `NO_COLOR` | plain text, no colour |

Scripts and agents can also set `EVADO_TOKEN`, `EVADO_SERVER`, `EVADO_LOCAL`
and `EVADO_JSON`; see [AGENTS.md](../AGENTS.md).

## How the token is kept

The app stores the token in `config.json` with `0600` permissions and writes
it through a temporary file, so an interrupted save cannot corrupt it. A
`config.json` that cannot be read, after a hand edit gone wrong for example,
is never written over: the app, `-save`, `-check` and every command stop with
an error naming the file until it is fixed or moved aside.
