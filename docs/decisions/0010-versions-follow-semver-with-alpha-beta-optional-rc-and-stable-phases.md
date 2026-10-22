---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0010. Versions follow SemVer, with alpha, beta, optional rc and stable phases

**Status:** Accepted, 2026-10-02.

**Context.** A version tells a reader both how big a change is and how settled it is. SemVer covers the first; the phases cover the second.

**Decision.**

- **Major, minor and patch follow SemVer:** a breaking change bumps major, a feature bumps minor, and anything else bumps patch. `komodo/rules/backlog.md#choosing-a-version` is the rule.
- **A prerelease carries a phase:** `-alpha.n` while the shape can move, `-beta.n` once feature-complete with only fixes landing, and `-rc.n` only when the owner asks. Stable has no suffix and is cut by the owner.
- **`komodo release` publishes from the owner's machine:** it cross-compiles every platform, runs the tests, writes checksums and creates the GitHub Release. Product repos install a published release.
- **V1's numbering is a caveat.** The prototype's four releases are `1.0.0-alpha.1` to `.4`, V1 resumed at `alpha.5`, and the untagged `1.0.0-beta.1` heading is history, so V1's betas start at `beta.2`.

**Alternatives.**

- **Require an rc before every stable release.** A gate no V1 release needs.
- **Commit the binaries.** About 9 MB per platform per change, in git history forever.

**Consequences.**

- **Every existing tag stays valid,** and none is rewritten.
