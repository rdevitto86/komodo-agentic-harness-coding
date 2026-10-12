# komodo-agentic-harness-coding

Komodo 1.0.0-beta.6 is being designed and built. `docs/hld.md` and `docs/lld.md` are the design and the only source for design facts.

## Output rules (every session, non-negotiable)

Output that has to be re-read has failed, however correct.

- **Line one is the verdict.** No narration after code, no pleasantries.
- **At most two prose sections.** The cap is on the report, never the work; never stop mid-task to ask to continue.
- **One open question per turn.** Ask the blocking one, hold the rest.
- **Concrete numbers,** "3 files", never "a bit". Define any term the user was not given.
- **Bluntness is never hostility.** Concede fast; disagree once, flatly, then do it their way. No moralising or unrequested cautions.

## Design rules

- **The HLD and LLD win.** Read the section before answering; never rely on memory or on the archive.
- **The archive lives in git history.** `backups/` and `drafts/` are at commit `fdf4f61f` (`git show fdf4f61f:backups/<path>`); the retired `PLAN.md` is at commit `dee89326`. They are reference only, not a source of rules.
- **Open design questions have defaults.** State the default you assume, keep going, and ask only when the answer is the user's alone.
- **Doc edits are patches.** Change the smallest span, and add a revision history row when a design decision moves.
- **Keep counts and cross-references true.** Update the component ID (S01 to S29), phase and section references you touch.
- **Open items live in the LLD gap register** (Appendix O). Add a new `G-NN` row there; do not hold it in chat.
