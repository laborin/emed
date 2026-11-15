# Contributing to Emed

Thanks for helping. This document explain how we work on emed, so anybody (human or AI agent) can pick a task and deliver it in the same way.

## Setup

Requirements:

- Linux or macOS. Windows is not supported.
- Go. The exact toolchain is pinned in `go.mod` (`toolchain` line); with `GOTOOLCHAIN=auto` (the default) Go downloads it for you.
- `git`. Optional: `lazygit` and `claude` to try the integrations.

```sh
git clone git@github.com:laborin/emed.git
cd emed
make build
./emed
```

Linters and `govulncheck` don't need to be installed: the Makefile runs the pinned versions with `go run`.

## Daily workflow

1. Pick or open an issue. For anything bigger than a small fix, agree on the approach in the issue first.
2. Create a branch from an updated `main`:
   - `feat/<short-name>` for new features
   - `fix/<short-name>` for bug fixes
   - `refactor/`, `test/`, `docs/`, `ci/`, `chore/` for the rest
3. Make small, focused commits.
4. Run `make ci` before pushing. It runs the same checks than GitHub Actions.
5. Open a pull request against `main` and fill the template.
6. CI must be green and the PR needs a review before merge.
7. PRs are merged with **squash merge**, the branch is deleted after merge.

`main` is protected: no direct pushes and no force pushes. It must be always releasable.

## Commit messages and PR titles

We use [Conventional Commits](https://www.conventionalcommits.org/). Because we squash, the **PR title** becomes the commit in `main`, and CI checks the PR title format.

```
<type>(<optional scope>): <description in imperative>

feat(editor): add save as dialog
fix(terminal): keep scrollback after resize
refactor(ui): split key routing out of layout
```

Types: `feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci`, `chore`, `revert`. Add `!` after the type for breaking changes (`feat(config)!: ...`). Useful scopes: `editor`, `terminal`, `filemanager`, `search`, `claude`, `git`, `ui`, `config`.

Release notes are generated from these titles, so write them for the users.

## Code guidelines

- Format with `gofmt` (`make fmt`). Imports are grouped by `goimports`, with emed packages last.
- Clear separation of concerns, no god objects, clean interfaces, proper error handling. Never ignore an error silently: handle it, return it, or show it to the user.
- Keep UI code (`internal/ui`) thin. Logic that don't need tview belongs in `internal/core` or its own package, where it is easy to test.
- Prefer short docblocks (godoc) over inline comments. The code should explain itself.
- Respect the existing patterns; if one looks wrong, propose the change in an issue or PR instead of mixing styles.

## Linting

`make lint` runs golangci-lint (config in `.golangci.yml`) **only on code changed since `origin/main`**. The old code still have issues (see `make lint-all`), cleaning them is planned in [docs/ROADMAP.md](docs/ROADMAP.md). Rule: new or modified code must not add new issues.

## Tests

- `make test` for a quick run, `make test-race` like CI (Linux and macOS).
- Every bug fix should come with a regression test, every feature with tests of its logic.
- UI behavior can be tested headless with `tcell.SimulationScreen`, driving real key events through the real widgets. See `internal/ui/starters_test.go` and `internal/ui/searchbar_test.go` as examples.
- Some things still need a manual test in a real terminal (rendering, paste, mouse, colors). Describe what you tested in the PR (OS, terminal emulator, local or SSH).

## The vt10x fork

Terminal emulation uses our fork [github.com/laborin/vt10x](https://github.com/laborin/vt10x). To change it:

1. Make the change in the fork, with tests, and push it to its `master`.
2. In emed: `go get github.com/laborin/vt10x@<commit>` and `go mod tidy`.
3. Open one PR in emed that bumps the version and adapts the code. Mention the fork commit in the description.

Dependabot does not update the fork, it is always bumped by hand.

## Releases

Versions follow [SemVer](https://semver.org/). While we are in `0.x`, minor versions can have breaking changes.

1. Make sure `main` is green.
2. Tag it: `git tag -a v0.6.0 -m "v0.6.0" && git push origin v0.6.0`.
3. The `Release` workflow runs the tests and GoReleaser, creating a **draft** GitHub release with the binaries (Linux/macOS, amd64/arm64), checksums and the changelog.
4. Review the draft notes and publish it.

The version is injected from the tag at build time; there is no version number to edit in the code.

## Reporting bugs

Use the bug template. For rendering or input bugs the terminal emulator, `$TERM`, OS and if you are over SSH are very important, many bugs only happen in some combinations.
