# Releasing

A release is a version tag. Pushing `vX.Y.Z` runs `.github/workflows/release.yml`,
which tests the source, builds all seven targets, signs and notarizes the two
macOS binaries, packages archives with SHA-256 checksums, and publishes a
GitHub release. It only runs in the public `nerdlabz/evado-tui` repository.

The workflow refuses a tag that is not a plain `vX.Y.Z` or does not match
`VERSION`, stamps that version into the binaries and the Windows file
properties, and checks that the built Linux and signed macOS binaries both
report `evado X.Y.Z` before anything is published.

## Runners

CI and the release build run on Blacksmith's 2 vCPU Linux and Windows
runners, and on GitHub's macOS runners. Pull requests from forks use
GitHub-hosted runners only. To move everything to GitHub-hosted runners, for
instance when Blacksmith's minutes run out, and back:

```sh
mise run ci:github       # every job on GitHub-hosted runners
mise run ci:blacksmith   # back to Blacksmith
mise run ci:runners      # which one is in use
```

These set and delete the repository variable `CI_RUNNERS`, so they need write
access to the repository. A run already queued keeps its runners; re-run it
to move it.

The macOS signing job always runs on GitHub's runners, because it holds the
release keys.

## Cutting a release

1. Set `VERSION` to the new version, without the leading `v`, and merge it.
   The tag must match it, or the workflow stops.
2. If `go.mod` or `go.sum` changed since the last release, recompute
   `vendorHash` in `nix/source.nix`. The hash covers the Go vendor tree, not
   the release binary.
3. Tag the merge commit and push the tag:

   ```sh
   git tag v1.0.0
   git push origin v1.0.0
   ```

4. When the release is published, update the two package channels below with
   the release's generated `evado.rb` and `release.json`.

The same packaging can be run locally:

```sh
EVADO_BUILD_VERSION=1.0.0 mise run build-all
mise exec -- python3 scripts/third-party-notices.py > dist/THIRD_PARTY_NOTICES.txt
python3 scripts/package-release.py 1.0.0 \
  --repository nerdlabz/evado-tui \
  --notices dist/THIRD_PARTY_NOTICES.txt --output dist/release-1.0.0
```

The packager takes already-built binaries; it never invokes a compiler. It
refuses an existing output directory, requires all seven binaries, and writes
only the executable, the license and dependency notices into each archive.
Archives use fixed timestamps, so the same binaries give the same archives.
`SHA256SUMS` covers the archives, the generated Homebrew formula and the Nix
release manifest.

## macOS signing and notarization

The `sign-macos` job runs in the `release` environment and needs these
secrets there. It fails before signing anything if one is missing.

| Secret | Value |
| --- | --- |
| `MACOS_CERTIFICATE` | The Developer ID Application certificate and its private key, exported from Keychain Access as `.p12`, then base64-encoded |
| `MACOS_CERTIFICATE_PASSWORD` | The password chosen when exporting the `.p12` |
| `MACOS_SIGNING_IDENTITY` | The identity's full name, as `security find-identity -v -p codesigning` prints it |
| `APPLE_API_KEY` | An App Store Connect API key (`.p8`) with the Developer role, base64-encoded |
| `APPLE_API_KEY_ID` | That key's ID |
| `APPLE_API_ISSUER_ID` | The issuer ID shown above the keys list in App Store Connect |

```sh
gh secret set MACOS_CERTIFICATE -R nerdlabz/evado-tui --env release < <(base64 -i developer-id.p12)
gh secret set MACOS_CERTIFICATE_PASSWORD -R nerdlabz/evado-tui --env release
gh secret set MACOS_SIGNING_IDENTITY -R nerdlabz/evado-tui --env release
gh secret set APPLE_API_KEY -R nerdlabz/evado-tui --env release < <(base64 -i AuthKey_XXXXXXXXXX.p8)
gh secret set APPLE_API_KEY_ID -R nerdlabz/evado-tui --env release
gh secret set APPLE_API_ISSUER_ID -R nerdlabz/evado-tui --env release
```

Binaries are signed with the hardened runtime and a secure timestamp, under
the identifier `app.evado.cli`. A bare executable cannot have a notarization
ticket stapled to it, so Gatekeeper checks it online the first time it runs.

## Homebrew

The formula lives in [nerdlabz/homebrew-tap](https://github.com/nerdlabz/homebrew-tap)
at `Formula/evado.rb`. Replace it with the release's generated `evado.rb`
through a PR. `packaging/homebrew/evado.rb.in` is the template it comes from.

```sh
brew install nerdlabz/tap/evado
brew test nerdlabz/tap/evado
evado -version
```

## Nix

Commit the release's generated `release.json` to `packaging/nix/release.json`
through a PR. It binds each platform to its archive URL and SHA-256. The flake
in `packaging/nix` installs those binaries; the root flake builds from source.

```sh
nix run 'github:nerdlabz/evado-tui?dir=packaging/nix' -- -version
nix build github:nerdlabz/evado-tui#evado
```

The lock files pin nixpkgs 26.05, which still supports Intel Macs. Before
moving to a newer nixpkgs, check all four platforms. Evaluate without building
using `nix flake check --all-systems --no-build`.
