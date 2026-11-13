# Homebrew formula

`evado.rb.in` installs the release binary on macOS and Linux, for ARM64 and
x86-64. It installs prebuilt archives rather than building from source.

`scripts/package-release.py` replaces these tokens to make the `Formula/evado.rb`
published in `nerdlabz/homebrew-tap`. It takes the release repository's name as
configuration.

| Token | Value |
| --- | --- |
| `@VERSION@` | Release version without its leading `v` |
| `@RELEASE_REPOSITORY@` | Public binary repository, in `owner/repo` form |
| `@DARWIN_ARM64_SHA256@` | SHA-256 of the macOS ARM64 archive |
| `@DARWIN_AMD64_SHA256@` | SHA-256 of the macOS x86-64 archive |
| `@LINUX_ARM64_SHA256@` | SHA-256 of the Linux ARM64 archive |
| `@LINUX_AMD64_SHA256@` | SHA-256 of the Linux x86-64 archive |

Each archive is named `evado_VERSION_OS_ARCH.tar.gz` and contains an executable
named `evado`. Its `-version` output must be `evado VERSION`. The download URL
uses the tag `vVERSION`. Generate checksums from the exact uploaded archives,
and refuse publication if any token remains. A release must provide all four
archives before the tap changes.

## Verify a generated formula

Run these checks on the generated `.rb` file, not the `.in` template:

```sh
ruby -c Formula/evado.rb
brew style Formula/evado.rb
brew trust --formula nerdlabz/tap/evado   # Homebrew loads only trusted tap formulae
brew audit --strict --formula nerdlabz/tap/evado
```

Once the release is published, check installation and the formula test on
macOS and Linux, on both architectures:

```sh
brew install nerdlabz/tap/evado
brew test nerdlabz/tap/evado
evado -version
```

The formula test uses `-local` and a temporary `EVADO_CONFIG` profile. It adds and
lists a todo without an account or a network request. Syntax, style and formula
loading alone do not verify the downloaded archive or a platform's executable.

The formula uses Homebrew's [platform and architecture blocks](https://docs.brew.sh/Formula-Cookbook#handling-different-system-configurations)
and [formula test block](https://docs.brew.sh/Formula-Cookbook#add-a-test-to-the-formula).
