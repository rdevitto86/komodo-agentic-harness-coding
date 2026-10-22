---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0007. Sessions run sandboxed, offline for Go, and under process limits

**Status:** Accepted, 2026-10-02.

**Context.** The guard catches mistakes; it can never be a wall. Spike S1 (2026-09-26, macOS) found the sandbox blocks Go's module fetches unless weaker network isolation is on, which opens an exfiltration path, while a builder with modules already in the worktree passed `go vet` and `go test -race` offline. A test once forked 900 processes and exhausted a 24 GB machine.

**Decision.**

- **Line sessions run in the OS sandbox where the platform has one,** with the forge off the network allowlist.
- **A builder runs Go offline:** `GOPROXY=off`, with every Go cache inside the worktree. The conductor fetches modules outside the sandbox, and a task that needs a missing module fails fast, naming it.
- **Every station command and session runs under a watcher:** past 256 processes or 8 GB resident, it kills the whole tree. Nothing outlives what started it.
- **A session's temp root sits outside every repo,** so no test can walk up into a real worktree.

**Alternatives.**

- **Weaker network isolation for Go sessions.** Every Go session could then reach the network.

**Consequences.**

- **No Go session reaches the network,** and a runaway becomes a failed check, never a frozen machine.

**Open.**

- **No code runs `go mod download` yet,** so a product Go repo with dependencies can't build in a session.
- **Native Windows has no job object or watcher;** a timeout kills only the parent process.
- **Doctor doesn't pin `go.mod`'s `go` directive,** so a builder's `go get` can raise it unnoticed.
