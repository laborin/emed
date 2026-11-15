# Roadmap

Planned work, in order. Each item should become a GitHub issue when somebody start working on it.

## Phase 0: Development workflow

- [x] CI on GitHub Actions: gofmt, go mod tidy, go vet, golangci-lint (new issues), govulncheck, race tests on Linux and macOS, cross build.
- [x] Conventional Commits, PR title check, PR and issue templates, CODEOWNERS, Dependabot.
- [x] Version injected from git tags, GoReleaser releases (Linux/macOS, amd64/arm64).
- [x] Toolchain pinned in `go.mod` (go1.26.11, it fixes stdlib vulnerabilities reported by govulncheck).
- [x] Docs in the repo: CONTRIBUTING, ARCHITECTURE, this roadmap, AGENTS.md.
- [x] Windows dropped officially (the code is Unix only).
- [x] Branch protection on `main` (PR + 1 code owner review + green checks, linear history, no force push; admins can bypass), squash merge only, delete branch on merge.
- [ ] Tag `v0.5.0` and publish the first release with binaries.

## Phase 1: Fixes

- [ ] Save errors are ignored silently (`internal/ui/editor.go`, `SaveCurrentFile`): show them to the user. Risk of data loss.
- [ ] New buffers without path can not be saved: add a "Save as" dialog.
- [ ] Handle `SIGHUP` (SSH disconnect, terminal closed): today emed dies without cleanup, leaving the Claude lock file and not saving the session.
- [ ] Possible emoji width bug in the file tree (rows with 📂 📄 🖼️ shift the border one column, seen inside tmux). Confirm in a real terminal.
- [ ] `main` panics when the app fails to run; exit with a clean error and exit code instead.
- [ ] Clean the existing lint debt (`make lint-all`, ~180 issues: unchecked errors, unused code, staticcheck). Start with `errcheck` and `nilerr`, they can hide real bugs.
- [ ] Stale fsnotify watch on renamed directories.
- [ ] Close the manual tests still pending in the status doc (atomic rename reload on Linux, starters, session restore, theme, transparent background, gutter separator, hidden root).

## Phase 2: Technical debt

- [ ] Split `codeeditor.go` (~2800 lines) and `layout.go` (~1800 lines). First step: a command registry (action → handler + default key) to replace the big key switch statements.
- [ ] Tests for the pure packages with low coverage: `core/buffer`, `core/search`, `core/selection`, `filemanager/operations`, `terminal/scrollback`.
- [ ] Golden tests for terminal emulation: feed byte streams, compare the resulting grid.
- [ ] Headless end-to-end UI tests with `tcell.SimulationScreen` for the main flows (open, edit, save, close tabs, terminal lifecycle, Claude diff handoff).

## Phase 3: Features

- [ ] Crash and disconnect recovery: swap files for unsaved buffers.
- [ ] Fuzzy file finder.
- [ ] Command palette (built on the command registry).
- [ ] Configurable keybindings.
- [ ] `.editorconfig` support.
- [ ] Git change markers in the gutter and git status colors in the file tree.
- [ ] User themes and a light theme.
- [ ] Homebrew tap.
- [ ] Research: LSP support (go to definition, diagnostics).
