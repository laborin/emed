# Emed

## Project Status
This project is still an experiment.

I started it as a learning exercise and I use it daily for my own work. For me, it works good enough and I have been using it for months while building it.

But this is not production-ready software yet. You should expect bugs, weird behavior in some terminal apps, and breaking changes while I keep improving things.

It is provided as-is, without warranty.

## Motivation
I found myself wanting to code on my iPad, connected via SSh to a remote server. I don't like traditional terminal-based editors, they are too complex, I didn't wanted to remember dozens of keyboard shortcuts. Even micro was too complex, with all the panes and tabs and plugins. But I definitedly wanted more power than what nano could offer. Then, I imagined... I want a code editor that has a file manager on the left, a terminal on the bottom and a main area with tabbed documents, and intuitive keyboard shortcuts. I know, not original, this is the standard desktop IDE layout, but I could not find a single tool that offered this experience in the terminal without hours of initial configuration, learning and practice.

Then, I stumbled upon [tview](https://github.com/rivo/tview), and then made some research and found [chroma](https://github.com/alecthomas/chroma) and thought that creating my own TUI editor would be matter of just joining some pieces together (it was not). This was a great excuse to grab that Go book I purchased a couple of years ago when I was trying to get back to coding non-web stuff, and build something with a new language. This project was created as a learning exercise but aims to be a useful tool.

## Technical Description
Emed is built on top of the tview framework, which handles all the heavy lifting for the terminal UI. The layout is a simple flex container system that tview provides - nothing fancy, just three panels arranged how I wanted them. The file manager uses a custom horizontal-scrolling tree widget (htree) that I built from scratch because tview's TreeView couldn't handle long file names properly. It maintains the expansion state during refreshes and supports file operations like create, rename, delete, and clipboard operations.

The code editor is a custom tview primitive that handles text editing operations. Syntax highlighting is implemented using Chroma, with support for most languages. An early attempt was made with tree-sitter but it was too complex for such a tiny project like this. Before tree-sitter, the original, failed attempt was with Chroma, so the current implementation is the third attempt.

The terminal emulator has probably been the most fun and the hardest part to implement so far. I glued together creack/pty for pseudo-terminal handling and my fork of vt10x for VT100 emulation and escape sequence parsing. The tricky part was translating between vt10x's terminal state and tview's rendering system - especialy for colors, where I wrote a translation layer that handles ANSI colors (16 colors), 256 colors palette, RGB colors and even default FG and BG colors. Mouse events were another headache - I had to intercept and translate between tcell mouse events and terminal escape sequences. The scrollback buffer implementation was fun too, since I had to track when apps switch to alternate screen and vt10x was not very helpful to track lines that go out of screen, I added some helper code on my own fork to solve this. After a recent overhaul it got much more solid: no more lost bytes when multibyte characters land on chunk boundaries, wide characters (CJK, emoji) take their two columns like they should, terminal modes are read directly from the emulator state, the scrollback history no longer gets polluted by TUI redraws, and there is even system bell support with blinking tab titles. Complex TUIs like btop, htop, lazygit, Claude Code and emed itself render correctly now, even on big terminal windows.

## The Plan
- [x] Simple 3-pane layout with easy keyboard navigation.
- [x] File manager in left panel with basic file operation (rename, delete, create files and dirs, copy, move paste files).
- [x] Fully functional terminal emulator on bottom panel.
- [x] Code editor, able to save files, responsive to external file changes, ready for syntax-highlighting using tview's color system.
- [x] File watcher to keep track of external changes on the project directory. Keeps the file manager in sync and updates unedited open tabs with new content.
- [x] Refactor for better file organization, modularity and best practices.
- [x] Implement syntax highlighting. Final implementation was made with Chroma after finding out tree-sitter was too complex.
- [x] Implement project-wide and within-file search with regex support and advanced options.
- [x] Add some kind of integration with agentic AI tools.
- [x] Add support for LazyGit.
- [x] Soft line wrapping in the editor.
- [x] Deeper Claude Code integration: diff approval modal, multiple sessions at the same time, session info on each diff.
- [x] Terminal emulation hardening: wide chars (CJK/emoji), utf8 edge cases, clean scrollback history, system bell with blinking tab titles.
- [x] Per project terminal starters (F12) and session restore of open editor tabs.
- [ ] Next steps are in [docs/ROADMAP.md](docs/ROADMAP.md).

## Screenshots
Emed's initial view:
![Emed's initial view](assets/screenshot1.png)

Terminal panel open, running htop. Emed can handle medium complexity TUI apps on its embedded terminal.
![Emed's terminal](assets/screenshot2.png)

A code file open with syntax highlighting:
![Emed's editor panel](assets/screenshot3.png)

Inception. Running an emed instance in an emed terminal panel:
![Emed running emed](assets/screenshot4.png)

Running Claude Code in an emed terminal:
![Claude Code on emed](assets/screenshot5.png)

~~Struggles to render some specific apps, like OpenCode. This is going to improve by adding support for a wider set of escape sequences:~~ This has been greatly improved. It can now handle super complex TUIs like btop.
![OpenCode on emed](assets/screenshot6.png)

A more recent version of Emed, with improved file manager and basic claude code integration showing a diff to approve/reject:
![Emed 0.2](assets/screenshot7.png)

Beautiful Btop TUI running inside emed's terminal:
![Btop](assets/screenshot8.png)

Inline Git blame:
![Inline git blame](assets/screenshot9.png)

Full Git Blame modal (Ctrl+B):
![Git blame](assets/screenshot10.png)

Lazygit modal shown right in Emed:
![Lazygit](assets/screenshot11.png)

Mission accomplished, using Emed with my iPad (Blink):
![Ipad](assets/screenshot12.png)

## Usage
Run `emed` inside your project directory, or pass a path:

- `emed` - opens the current directory as the project.
- `emed /path/to/project` - opens that directory as the project.
- `emed file.txt` - single file mode: a reduced, editor-only layout for quick edits.
- `emed -c` (or `--claude`) - opens the project and launches Claude Code in a terminal tab.
- `emed -o` (or `--opaque`) - use fixed background colors instead of the terminal background.
- `emed -v` / `emed -h` - show version / help.

## Colors and transparent terminals
By default the main surfaces (panels, editor, tab bars and the embedded terminal) use the terminal own default background, so semi-transparent terminal setups keep their effect. Modal dialogs stay opaque on purpose, for readability. Note that emed's text colors assume a dark background: on light terminal themes you probably want the fixed colors instead.

To use fixed dark backgrounds everywhere, run with `--opaque` or set it permanently in `~/.emed/config.json`:

```json
{ "useTerminalBackground": false }
```

## Claude Code integration
Emed speaks the same IDE protocol that Claude Code uses with VS Code and JetBrains. There is nothing to configure: when you run `claude` inside an emed terminal tab, it detects the editor and connects automatically.

What you get:
- Claude sees your open files and your current selection as context.
- When Claude wants to edit a file, emed shows a diff modal so you can approve or reject the change before it is applied. The header shows the file, the project and which Claude session sent it.
- Several Claude sessions can run at the same time in different terminal tabs, all connected to the same emed instance.
- F9 launches Claude Code (or jumps to its tab if already running).

## Terminal starters
Every project can define "starters": named terminal commands you run often, like a dev server or a test watcher. Press F12 to open the starters list, pick one, and emed opens a terminal tab at the project root running that command. The tab takes the starter name as title.

- Starters marked as "unique" only run one instance: while its terminal is alive, the starter shows as "(running)" and selecting it jumps to its tab instead of launching another one.
- From the same list you can add new starters (A) or delete them (D).
- Config lives in `.emed/starters.json` at the project root, so you can commit it and share it with your team.

## Project state
When you open a project again, emed reopens the files you had open in the editor, including which one was active. This state is saved in `.emed/session.json`. Files outside the project root are not remembered, paths are stored relative to the root so the state survives when the project is moved or synced between machines. You probably want `.emed/session.json` in your `.gitignore` (keep `starters.json` if you want to share it).

Both features only exist in project mode, single file mode does not create any `.emed` directory.

## Key bindings
### Global
- Ctrl+A: Focus file manager.
- Ctrl+E: Focus code editor.
- Ctrl+T: Focus terminal, creates a new terminal tab if none exists.
- F1: Shows the help screen.
- F2: Cycles focus through file manager / editor / terminal.
- F3 / F4: Previous / next tab on the focused panel (editor or terminal).
- Ctrl+L or F7: Cycles through pre-defined editor/terminal size proportions.
- Ctrl+P or F6: Toggles terminal placement, bottom or right.
- F8: Shows memory and file watcher stats.
- F9: Launches Claude Code (or jumps to its tab if already running).
- F12: Terminal starters for the project (not available in single file mode).
- Ctrl+Q: Exit emed.

### File Manager
- Arrow keys: browse project file tree.
- Enter: Opens the highlighted file on an editor tab.
- Ctrl+F: Project-wide search, with regex and replace support. Alt+C / Alt+W / Alt+R toggle case, whole word and regex; results show a highlighted fragment of each match; Ctrl+Enter replaces all.
- Ctrl+N: Creates a new file. Ctrl+Alt+N creates a new directory.
- Ctrl+R: Renames the highlighted file or directory.
- Ctrl+D: Deletes the highlighted file or directory.
- F5: Refreshes the file tree.
- Ctrl+I: Shows file information panel.
- Ctrl+C: Copy reference of highlighted file to internal clipboard.
- Ctrl+X: "Cuts" highlighted file to internal clipboard (for moving).
- Ctrl+V: Pastes (copies or moves) the file in internal clipboard to the highlighted position.

### Editor
- Ctrl+S: Save file.
- Ctrl+F: Search within the file. Compact two-row bar: Enter next match, Shift+Enter previous, Enter in the Replace field replaces and advances, Alt+A replaces all, Alt+C / Alt+W / Alt+R toggle case, whole word and regex.
- Ctrl+Z / Ctrl+Y: Undo / redo.
- Ctrl+C: Copy highlighted text to system clipboard.
- Ctrl+X: Cuts highlighted text to system clipboard.
- Ctrl+V: Pastes from system clipboard to cursor position.
- Ctrl+Shift+Left/Right: Browses through open tabs.
- Ctrl+W: Closes current tab.
- Ctrl+B: Git Blame.
- Ctrl+G: Lazygit.
- F10: Toggles soft line wrapping.
- Word-by-word, line and pgup-pgdown navigation implemented but still erratic or inconsistent between platforms.

### Terminal
- Ctrl+W: Closes current tab.
- Ctrl+T: Opens a new terminal tab.
- Ctrl+Shift+Left/Right: Browses through open tabs.
- Mouse wheel: Scrolls through the terminal history. Any click returns to the live view.
- Alt+T: Toggles passthrough mode, where all keys and key combinations (except Alt+T) are sent to the terminal.

## Known limitations
- Still experimental. You can hit crashes in some edge cases.
- Embedded terminal emulation handles complex TUIs correctly now, but apps using exotic escape sequences could still find a corner case.
- Key handling can behave different depending on terminal emulator and OS.
- Claude Code integration works, but it is still evolving and may break between versions.

## Installing
Download the binary for your platform from the [releases page](https://github.com/laborin/emed/releases) (Linux and macOS, amd64 and arm64), or build it with Go:

```sh
go install github.com/laborin/emed/cmd/emed@latest
```

Emed runs on Linux and macOS. Windows is not supported (the embedded terminal and some file APIs are Unix only).

## Building
You need the Go version declared in `go.mod`; with a recent Go installed, the right toolchain is downloaded automatically.

- `make` or `make build` - builds the emed binary, with the version taken from the latest git tag
- `make install` - builds and installs to /usr/local/bin (needs sudo)
- `make build-all` - cross builds for Linux and macOS (amd64 and arm64) into `build/`
- `make dev` - builds with debug symbols for development
- `make help` - lists all the targets

## Contributing
Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow (branches, commits, pull requests, tests) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for an overview of the code. Planned work is in [docs/ROADMAP.md](docs/ROADMAP.md).

## FAQ

**Q: Why?**  
A: One of my new year resolutions for 2025 was to code for fun again. A simple, easy to use terminal editor with decent features that does not require hours of reading and practice to use is a real need that I have.

**Q: Can I use it alerady?**  
A: Yes, you can use it. I do. But I still consider it experimental.

**Q: Why "Emed"?**  
A: My name is Emmanuel, and it's my editor. But we could give the acronym other meanings, like "Extremely Mediocre Editor".

**Q: Why Go?**  
A: I had to put some unused knowledge in practice.

## License
MIT. See [LICENSE](LICENSE).

Made with ❤️ in Sonora 🌵

