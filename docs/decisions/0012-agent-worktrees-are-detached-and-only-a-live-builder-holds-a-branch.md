---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0012. Agent worktrees are detached, and only a live builder holds a branch

**Status:** Accepted, 2026-10-02.

**Context.** On 2026-10-02 a person merged a PR, then `git branch -d docs/hld-lld-and-decision-files` failed: an ad hoc session had that branch checked out in `.komodo/wt/docs-hld-lld`. Git refuses to check out or delete a branch any linked worktree holds, and only `komodo sync` freed it. Branch claims bound the person's own sessions for 12 hours while exempting builders, the reverse of the intent. Nothing released either lock when work was approved and pushed.

**Decision.**

- **Every worktree an agent or the line makes is detached.** `komodo worktree add <branch>` cuts one for ad hoc work and records the branch as `komodo.branch` in its worktree config.
- **Komodo never checks out, moves or deletes a branch under `refs/heads/`.** The line keeps each tip at `refs/komodo/<branch>` and pushes it to `refs/heads/<branch>` on `origin`.
- **A working builder holds a lease on its branch; nothing else holds a lock.** The line takes it when a stage starts writing, in `komodo/leases/<branch>.json` under the git common dir, so every worktree finds it. While it lives, the guard refuses a session's push to that branch and the gate's pre-push refuses a person's.
- **The lease ends mechanically:** the line's push drops it, and it lapses 2 hours after it was taken or as soon as its holder process exits. A later stage that writes again takes a fresh lease. Branch claims and `komodo guard release` are removed.
- **The global tier refuses attaching a branch in a linked worktree:** `git worktree add` without `--detach`, and `git checkout` or `git switch` onto a branch there. It also refuses `update-ref`, `checkout -B` and `switch -C` on a critical ref, naming `komodo worktree add` or `--detach`.

**Alternatives.**

- **Detach, but advance `refs/heads/<branch>` locally.** Komodo would race a person's own commits and deletes on the same ref.
- **One clone per worktree.** No shared refs, but every worktree listing and hook install changes, and a raw `git worktree add` still pins.
- **Free the pin on merge, push or session end.** Each depends on timing, and a crash leaves the pin.

**Consequences.**

- **`komodo worktree add` is a complete ad hoc workflow:** it cuts the detached worktree and leaves it free to push the branch it tracks, by the command it prints; only a builder's worktree refuses a push.
- **A person can always check out, commit to or delete any local branch.** Only a push to a branch a builder is working on is refused, for at most 2 hours.
- **A line branch appears locally after a fetch,** and `refs/komodo/*` holds unpushed work until it ships or is abandoned.
- **The line's merged worktrees leave no local branch to delete;** REQ-46's "branches" now means the `refs/komodo` tips.
