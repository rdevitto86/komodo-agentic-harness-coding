---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0002. The conductor is one Go binary, for determinism and zero setup

**Status:** Accepted, 2026-10-02.

**Context.** The prototype was Python scripts. Every host needed an interpreter, a virtual environment and a shell shim, every hook call started a fresh interpreter, and behaviour drifted with each machine's setup.

**Decision.**

- **The conductor, its checks and its hooks are one Go binary built from the standard library alone.** A rebuild of one commit is byte-identical.
- **One native binary per platform:** macOS, Linux and Windows run it with nothing to install beyond git.
- **One install script per platform, `install.sh` or `install.ps1`, sets a machine up in one step:** the binary on PATH, the orchestrator layer, the current repo initialised, and doctor run. Running it again updates.
- **The binary decides; models read markdown.** Rules, roles, skills and policy are markdown or JSON under `komodo/`, and a model reads only the brief the binary fills from them.
- **Only `internal/mount/<host>/` names a host,** behind one contract, so a new host is a new directory.

**Alternatives.**

- **Keep Python.** An interpreter on every host, and start-up cost on every hook call.
- **Rust.** It fits as well, but the working code was Go, and Go cross-compiles with two environment variables.

**Consequences.**

- **A dependency is a design change** and needs its own decision.
- **WSL2 is a footnote.** Windows runs natively first. Where WSL2 is present, the Linux binary inside it also gets the host's OS sandbox, which doesn't run natively; doctor says when a machine has none.

**Spikes.** S6: does the line run natively on Windows 10 and 11 with Git for Windows, and inside WSL2? Its result is recorded here once it runs.
