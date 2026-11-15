# Architecture

Emed is a terminal code editor written in Go, with an IDE-like fixed layout: file tree on the left, tabbed editor in the center, tabbed terminal at the bottom (or right). This document give a map of the code and the main design decisions. Keep it updated when they change.

## Stack

| Concern | Library |
|---|---|
| UI framework | [rivo/tview](https://github.com/rivo/tview) over [gdamore/tcell](https://github.com/gdamore/tcell) |
| Syntax highlight | [alecthomas/chroma](https://github.com/alecthomas/chroma) |
| PTY | [creack/pty](https://github.com/creack/pty) |
| Terminal emulation | [laborin/vt10x](https://github.com/laborin/vt10x) (our fork) |
| File watching | [fsnotify](https://github.com/fsnotify/fsnotify) |
| Claude Code IDE protocol | [gorilla/websocket](https://github.com/gorilla/websocket) |

## Packages

| Package | Responsibility |
|---|---|
| `cmd/emed` | Entrypoint: CLI flags, startup mode (project or single file), app lifecycle. |
| `internal/config` | Constants, version, user config (`~/.emed/config.json`). |
| `internal/theme` | Semantic color roles used by every widget. Never hardcode colors in the UI. |
| `internal/core/*` | UI-independent logic: text `buffer`, `selection`, `search`, `syntax` highlighter, system `clipboard`, `pathutil` containment checks. |
| `internal/filemanager/*` | File operations (create, rename, delete, copy, move) and the internal file clipboard. |
| `internal/terminal/*` | PTY handling and scrollback. |
| `internal/git` | Git commands used by the UI (blame, repo detection). |
| `internal/claude` | Claude Code IDE integration: websocket server, auth, lock files in `~/.claude/ide`, tool handlers (diffs, open files, selection). |
| `internal/project` | Per-project state in `.emed/` (terminal starters, session restore). |
| `internal/logging` | File logger. |
| `internal/ui` | All tview widgets: `Layout` (root, focus, keybindings), editor + `codeeditor`, `htree` file tree, terminal tabs over vt10x, modals, diff view, search, lazygit, status bar. |
| `internal/ui/components` | Reusable UI parts, like the base tab manager. |

Dependency direction: `cmd` → `ui` → (`core`, `filemanager`, `terminal`, `git`, `claude`, `project`, `config`, `theme`). The non-UI packages must never import `ui` or tview (`theme` is the only exception, it maps roles to tcell/tview styles).

## Main design decisions

- **Fixed layout.** Normal mode is the 3-panel layout; single file mode (`emed file.txt`) is a reduced editor-only layout and never creates `.emed/`.
- **Buffer is the source of truth.** Soft line wrap is only a visual layer over real lines (cursor, mouse and scroll map through the wrap map), saved content is never changed by it.
- **One diff viewer.** Claude diffs use the internal diff view. Diffs that arrive while another modal is open are queued and shown when the screen is free.
- **Claude integration is local only.** The websocket server listens on `127.0.0.1`, checks the auth token before the upgrade, accepts several concurrent Claude sessions and removes stale lock files of dead emed processes.
- **Path safety.** File operations use explicit containment checks (`core/pathutil`), not string prefixes.
- **External changes.** fsnotify events are debounced; clean tabs reload, dirty tabs enter a conflict flow, renames are followed by pairing inodes.
- **Modal focus.** Custom modal primitives embed `tview.Flex` (not `tview.Box`) so tview focus chain can be walked; otherwise focus gets stuck in the first widget.
- **Terminal emulation lives in the fork.** UTF-8 across chunks, wide chars, scrollback rules and bracketed paste are implemented in vt10x; emed reads terminal modes from the emulator state, never by scanning bytes.
- **Theme.** By default panels use the terminal default background (transparency); modals are always opaque. `--opaque` or `useTerminalBackground: false` turns it off.

## Runtime files

| Path | Content |
|---|---|
| `~/.emed/config.json` | User config. |
| `<project>/.emed/starters.json` | Terminal starters, can be committed. |
| `<project>/.emed/session.json` | Open tabs, local, should be gitignored. |
| `~/.claude/ide/<port>.lock` | Claude Code discovery lock, removed on exit. |

## Debugging

- `EMED_PPROF_PORT=6060 emed` enables pprof.
- Other env vars: `EMED_ENABLE_PASTE`, `EMED_DISABLE_PASTE`, `EMED_CLIPBOARD`.
- `make dev` builds with debug symbols for `dlv`.

## Known risks

- `internal/ui` has very big files (`codeeditor.go`, `layout.go`) with too many responsabilities; splitting them is in the roadmap.
- Renamed directories keep a stale fsnotify watch until restart.
- Modals do not stack in general, only diffs are protected by the queue.
- Terminal behavior can vary between terminal emulators.
