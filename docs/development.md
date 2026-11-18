# Development

[CONTRIBUTING.md](../CONTRIBUTING.md) covers how to propose a change. This
page is the working reference.

## Tasks

```sh
mise trust
mise run test       # unit and integration tests
mise run check      # gofumpt + go vet + golangci-lint
mise run build      # host binary, stamped with the git version
mise run build-all  # every release target into dist/
```

[releasing.md](releasing.md) covers tagging a release and updating Homebrew
and Nix.

## Layout

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

## The fake server

There is a fake server so the UI can be exercised without an account:

```sh
mise x -- go run ./cmd/fakeserver -addr 127.0.0.1:8787 &
EVADO_CONFIG=/tmp/evado-dev.json mise x -- go run ./cmd/evado \
  -server http://127.0.0.1:8787/api/v1 -token evt_dev
```

It answers only requests that carry its token, `evt_dev` unless you start it
with `-token`, and it listens only on a loopback address unless you add
`-remote`. It handles one request at a time.

## Tests

The integration tests drive the real Bubble Tea model against a fake API
through a fake terminal, asserting on rendered output: `internal/tui` covers
navigation, forms, confirmations and error handling; `internal/api` covers
request shapes, error mapping and rate limits.

## The README screenshot

`assets/screenshot.png` is the app itself running `evado -demo`, recorded by
[vhs](https://github.com/charmbracelet/vhs) from `assets/screenshot.tape`.
When the Todos tab changes how it looks, remake it:

```sh
brew install vhs       # once; it brings ttyd and ffmpeg
mise run screenshot    # builds evado, then writes assets/screenshot.png
```

It needs the JetBrains Mono Nerd Font installed. The picture shows today's
dates, so it changes a little on every run.

## The Windows icon

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
