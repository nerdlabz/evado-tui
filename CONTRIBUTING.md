# Contributing to Evado

Thanks for helping. Bug reports, ideas, docs fixes and code are all welcome.

## Before you write code

Say so on the issue first, and wait for a maintainer to reply. That goes for
small fixes too. If there is no issue for what you have in mind, open one.

A pull request that arrives without that exchange is closed without review,
however good it is. Two reasons: it stops two people doing the same work, and
it tells us there is a person on the other end who will still be there when
the review has questions.

Typo and wording fixes to the docs are the exception. Send those straight in.

## Getting set up

You need [mise](https://mise.jdx.dev), which installs the pinned Go and lint
tools for you.

```sh
mise trust
mise run test       # unit and integration tests
mise run check      # gofumpt, go vet and golangci-lint
mise run build      # dist/evado
```

You don't need an Evado account to work on the client. `evado -demo` runs on
sample todos, `evado -local` works on a file on your computer, and
`cmd/fakeserver` stands in for the service.
[docs/development.md](docs/development.md) shows how to run against it, and
lays out where the code lives.
[AGENTS.md](AGENTS.md) documents every CLI command, flag and exit code.

## Making a change

- Put account behaviour in `internal/ops` and expose it from
  `cmd/evado/cli.go`. Interface changes go in `internal/tui`.
- Add or update tests with the change. The TUI tests drive the real model
  against a fake API, so most behaviour can be tested without a terminal.
- Keep formatting to `gofumpt`. Comments say why, not what, and match the
  surrounding code.
- If a flag, command, key or output changes, update the docs in the same
  pull request: `docs/using.md` for keys and behaviour, `docs/configuration.md`
  for flags and settings, AGENTS.md for the command line, and the README only
  if it changes what a newcomer does first.
- Keep a pull request to one change. Small ones are reviewed sooner.

Pull requests are squash-merged, and the PR title and description become the
commit message. Write them so they would make sense in `git log` a year from
now: what changed and why.

## Using AI tools

You're welcome to use AI tools to help with a contribution. We use them too.
What we don't take is work nobody is behind: an agent pointed at an issue list,
opening pull requests on its own. We hold the result to the same standard as
anything else, and ask for a few things:

- **A person opens every issue and pull request**, from their own account, and
  answers review comments in their own words. Issues or pull requests opened
  by an agent or bot on its own will be closed, and the account may be blocked.
- **Say what you used.** The pull request template asks which AI tool and
  model helped, if any. "None" is a fine answer. Leaving it blank is not.
- **Use a current frontier model.** Output from small or older models tends to
  look plausible and be wrong in ways that take longer to review than to
  write.
- **You are the author.** Read every line, run the tests, and be ready to
  explain any part of the change. If you couldn't defend it in review, it
  isn't ready.
- **Report what you saw.** In an issue, describe what happened on your machine.
  Please don't paste an AI's analysis you haven't checked.
- **Leave tool attribution out of commit messages.** The pull request
  template is the place for it.

Pull requests that look unreviewed, large and unexplained, unrelated to an
issue, or opened minutes after forking with no word on the issue may be closed
without a detailed review.

## Reporting bugs

Use the bug report form, and include `evado -version`, your platform and
terminal, and whether you were signed in or using todos on this computer.
Security problems go through [private reporting](SECURITY.md) instead.

## Conduct and license

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
By contributing, you agree that your contribution is licensed under the
[MIT License](LICENSE), like the rest of the project.
