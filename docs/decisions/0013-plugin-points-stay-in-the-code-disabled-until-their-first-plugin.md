---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0013. Plugin points stay in the code, disabled, until their first plugin

**Status:** Accepted, 2026-10-03.

**Context.** REQ-42 asks for three plugin points shipped disabled: notifiers, tool packs and stage hooks. The manifest format, the loader in `internal/plugin` and `internal/conductor/plugins.go`, and doctor's listing exist, but the conductor never calls the loader and no plugin has been written. A 2026-10-02 review filed TSK-11.20.2 to delete the loader as code nothing runs. The owner wants plugins later, starting with chat notifiers and cloud tool packs.

**Decision.**

- **The plugin points stay in the code, and 1.0 runs none of them.** The conductor does not call `LoadPlugins`, `Notify`, `Tools` or `Hook`.
- **Enabling a plugin on a machine changes nothing in 1.0.** `komodo doctor` lists each plugin type, and names an enabled plugin as one 1.0 does not run.
- **The first real plugin wires its point in,** with tests for that point, in the version that ships it.

**Alternatives.**

- **Delete the loader now and restore it later.** Less code in 1.0, but the format and loader would be rebuilt from history when the first plugin arrives.
- **Wire every point in for 1.0.** New runtime behaviour, untested against any real plugin, in the release meant to harden what exists.

**Consequences.**

- **REQ-42 holds as written:** the points exist and ship disabled, and doctor lists each one.
- **The loader stays maintained:** its tests run in the gate, and fixes to it, such as TSK-11.17.10, still land.
- **No plugin runs until a release wires its point,** so a manifest in `komodo/plugins/` is inert until then.
