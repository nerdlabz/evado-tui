# Installing Evado

Evado is one static binary with no runtime dependencies. Builds are published
for macOS, Linux and Windows, on ARM64 and x86-64, and for 32-bit ARM Linux.
The macOS binaries are signed and notarized.

## Homebrew

On macOS and Linux:

```sh
brew install nerdlabz/tap/evado
```

`brew upgrade evado` moves to a newer release.

## Nix

With flakes enabled, run it once or install it:

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

## A release archive

Download the archive for your platform from
[Releases](https://github.com/nerdlabz/evado-tui/releases), check it against
`SHA256SUMS`, and put `evado` somewhere on your `PATH`. This is the way to
install on Windows.

## With Go

With Go 1.25 or later:

```sh
go install github.com/nerdlabz/evado-tui/cmd/evado@latest
```

## Where it runs

The same build runs on Linux, macOS, Windows, WSL, a Raspberry Pi, or a
container.

`CGO_ENABLED=0` and no build tags: the binary has no libc dependency, so it
runs on any Linux, on macOS, and on Windows without a runtime. Text
measurement goes through `lipgloss`, which handles double-width characters and
combining marks, so CJK and emoji titles do not break the layout.

## Check it

```sh
evado -version
evado -demo      # sample todos, nothing saved
```
