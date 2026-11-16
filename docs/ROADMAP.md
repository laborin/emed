# Roadmap

Planned work lives in [GitHub issues](https://github.com/laborin/emed/issues), not in this file. Every issue has a priority label:

| Label | Meaning | Examples |
|---|---|---|
| [`priority: P1`](https://github.com/laborin/emed/labels/priority%3A%20P1) | Fix first: bugs, data loss, robustness | save errors ignored, SIGHUP cleanup, lint debt |
| [`priority: P2`](https://github.com/laborin/emed/labels/priority%3A%20P2) | Technical debt, tests, refactors | tcell migration, command registry, golden tests |
| [`priority: P3`](https://github.com/laborin/emed/labels/priority%3A%20P3) | New features | swap files, fuzzy finder, command palette |

Other labels: `bug`, `enhancement`, `tech-debt`, `needs-manual-test` (needs a check in a real terminal), `good first issue`.

## How to pick work

1. Take the highest priority open issue that is not assigned, and assign it to you.
2. Check the "Depends on" notes in the issue.
3. Follow [CONTRIBUTING.md](../CONTRIBUTING.md); the PR should say `Closes #<issue>`.

New ideas or bugs: open an issue with the templates, a maintainer set the priority.

## Done

- Development workflow (v0.5.0): CI, Conventional Commits, templates, Dependabot, versioning from git tags, GoReleaser releases, pinned toolchain, docs in the repo, branch protection. Windows support dropped (the code is Unix only).
