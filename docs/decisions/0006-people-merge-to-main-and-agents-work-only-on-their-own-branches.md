---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0006. People merge to `main`, and agents work only on their own branches

**Status:** Accepted, 2026-10-02. Amended by 0012, 2026-10-02.

**Context.** The forge is GitHub Free, and the developer's own token is the only credential, so the developer authors every PR and their own approval doesn't count; the merge is the human check. Hosted CI costs minutes and moves failure away from the person who can fix it.

**Decision.**

- **Only a person merges into `main`.** Each epic has a branch named `feat/<version>`, cut from `main` and opened as a draft PR into it.
- **A group branch cuts from its epic branch,** or from a group it depends on, and its PR targets that base. The conductor merges a reviewed, checked group PR into the epic branch.
- **Line sessions never touch git.** The conductor commits, pushes and opens PRs, and reads the credential only at Ship.
- **Ad hoc agents commit and push their own `<type>/<name>` branches only.** For every session, the guard refuses writing a critical ref, force pushing, rewriting pushed history, `--no-verify`, commit trailers and `gh pr merge`.
- **Every PR opens as a draft through `komodo pr create`,** which checks the title and applies the labels. A group PR keeps at most 20 files and adds at most 2,000 lines, 1,000 preferred; deletions are free, and an epic PR has no cap.
- **Nothing runs on the forge.** `komodo gate` runs before every commit and push with no model, and refuses when it finds no build check. It also installs hooks that rebuild the binary after a pull.

**Alternatives.**

- **Every group PR targets `main`.** One review per group, and no view of the whole epic.
- **CI on the forge, or a bot account.** Minutes and a second credential, for checks the gate already runs.

**Consequences.**

- **`main` gains one PR per epic,** reviewed at its final state.
- **A person can skip a hook with `--no-verify`;** the human merge is the backstop.
