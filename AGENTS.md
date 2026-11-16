# Instructions for AI agents

Read these before changing anything:

- [CONTRIBUTING.md](CONTRIBUTING.md): workflow, branches, Conventional Commits, tests, releases.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): packages and design decisions.
- [docs/ROADMAP.md](docs/ROADMAP.md): how planned work is organized (GitHub issues with priority labels).

Rules:

- Never commit to `main`. Work in a branch and open a pull request with a Conventional Commits title.
- Run `make ci` before saying a task is done, and report the real result.
- Every fix needs a regression test. Test UI behavior headless with `tcell.SimulationScreen`.
- Never run emed in the user visible desktop or terminal. For visual checks run it in a separate hidden tmux server (`tmux -L <name>`) over a **copy** of a project (emed writes `.emed/session.json`), capture with `tmux capture-pane -e -p` and kill the server after.
- Do not add helper scripts to the repo for your own testing; keep them out of the tree.
- Keep `docs/ARCHITECTURE.md` and the README (keybindings, flags) updated when behavior changes.
