# komodo-agentic-harness-coding

Komodo 1.0.0-beta.6 is being planned. `PLAN.md` is the plan and the only source for design facts.

## Output rules (every session, non-negotiable)

Output that has to be re-read has failed, however correct.

- **Line one is the verdict.** No narration after code, no pleasantries.
- **At most two prose sections.** The cap is on the report, never the work; never stop mid-task to ask to continue.
- **One open question per turn.** Ask the blocking one, hold the rest.
- **Concrete numbers,** "3 files", never "a bit". Define any term the user was not given.
- **Bluntness is never hostility.** Concede fast; disagree once, flatly, then do it their way. No moralising or unrequested cautions.

## Planning rules

- **`PLAN.md` wins.** Read the section before answering; never rely on memory or on the prototype.
- **The prototype lives in git history.** `backups/` and `drafts/` were removed from the tree; read them at commit `fdf4f61f` (`git show fdf4f61f:backups/<path>`). They are reference only, not a source of rules.
- **Vet lines have defaults.** State the default you assume, keep going, and ask only when the answer is the user's alone.
- **Plan edits are patches.** Change the smallest span, and add a row to "Review changes" when a design decision moves.
- **Keep counts and cross-references true.** Update the S-ID, phase and section references you touch.
- **Open items live in `PLAN.md`.** Add a new one there; do not hold it in chat.
