# Contributing to gatewaysshd

Thanks for your interest in contributing! This document explains how the
project is developed and released.

## Development setup

You need Go 1.25+ and docker (for the database-backed tests).

```
$ git clone https://github.com/ziyan/gatewaysshd.git
$ cd gatewaysshd
$ make build      # build into build/gatewaysshd
$ make test       # run all tests (starts a disposable postgres container)
$ make lint       # golangci-lint
$ make format     # gofmt the tree
```

`make test` exports `GATEWAYSSHD_TEST_DATABASE_HOST` pointing at the postgres
container it launches; without docker the database-backed tests skip and the
rest of the suite still runs.

## Code style

- Run `make format` and `make lint` before pushing; CI enforces both.
- Struct receivers are named `self`.
- No single-letter identifiers, no abbreviations (`command`, not `cmd`;
  `response`, not `resp`).
- Error strings are prefixed with the package name (`gateway: service not
  found`).
- Every goroutine starts with `defer deferutil.Recover()`.
- New behavior comes with tests. Database tests use
  `db/dbtest.AcquireDatabase`, which provisions an isolated, migrated
  database per test.

## Pull requests

- `master` is protected: changes land via pull request, and the `Changelog`,
  `Lint`, and `Test` checks must pass.
- Commit messages follow
  [conventional commits](https://www.conventionalcommits.org/):
  `feat: …`, `fix: …`, `chore: …`, etc. The release bot derives the next
  version from them (`feat` → minor, `fix` → patch; major bumps are never
  inferred).
- The PR template contains a **changelog block** delimited by
  `<!-- changelog:start -->` / `<!-- changelog:end -->` markers; CI locates
  the block by those markers, so keep them. Fill it in with one bullet per
  user-visible change, under the matching Keep-a-Changelog section
  (`### Added`, `### Changed`, `### Fixed`, …), and replace the placeholder
  heading and bullet. If your change has no user-visible effect (CI tweaks,
  docs, internal refactors), apply the `skip-changelog` label instead — CI
  fails production-code PRs that have neither.
- Opening a PR outside the GitHub web UI (e.g. `gh pr create --body`)
  bypasses the template — copy the changelog block, markers included, from
  [.github/pull_request_template.md](.github/pull_request_template.md).
- The `Changelog` check reads the description from the event that triggered
  CI, and description edits alone do not re-trigger it. After fixing the
  block, push a commit or close and reopen the PR to re-run the check.

## Releases

Releases are fully automated; there is nothing to do manually:

1. When a PR merges to `master`, the release bot (semantic-release) computes
   the next version from the commit messages.
2. It collects the changelog blocks of all merged PRs into a new section in
   [CHANGELOG.md](CHANGELOG.md), bumps `version.go`, commits
   `chore(release): x.y.z`, and pushes the `vx.y.z` tag.
3. The tag triggers the release workflow, which cross-compiles the binaries,
   generates checksums, and publishes a GitHub release whose notes are the
   new changelog section.

A major version bump requires deliberately dispatching the *Release Bot*
workflow with `forceReleaseType: major` (or pushing a `vX.0.0` tag by hand).
