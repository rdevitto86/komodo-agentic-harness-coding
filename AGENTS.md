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
- **The archive lives in git history.** `backups/` and `drafts/` are at commit `fdf4f61f` (`git show fdf4f61f:backups/<path>`); the retired `PLAN.md` is at commit `dee89326`. They are reference only, not a source of rules. Design v1 is frozen at tag `design/beta6-v1` (`git show design/beta6-v1:docs/lld.md`); the redesign lives on `feat/1.0.0-beta.6-v2`, and `feat/1.0.0-beta.6` stays at v1.
- **Undecided design is a `[TODO]`, never an assumption.** In the HLD and LLD, an open decision is written `[TODO: <the open decision> ([G-NN](#g-nn))]`, linked to its gap register row (`lld.md#g-nn` from the HLD); never fill it with a default.
- **Doc style:** descriptive and concise; no walls of text and no vague short labels; no acronyms in prose; no implied context.
- **Doc edits are patches.** Change the smallest span. The HLD and LLD carry no revision history or version numbers; git history records changes. Exception: the approved subsystem-first restructure moves whole sections, as a pure-move commit followed by a rewrite commit.
- **The HLD and LLD never share content.** The HLD holds purpose, ownership, interactions and decisions; the LLD holds types, interfaces, algorithms and limits. Link across, never copy.
- **Each subsystem has one LLD chapter with numbered child pages** (`### 2.1`, `### 2.2`), mirroring one short HLD subsystem section.
- **The HLD is a 20-minute read, a hard cap,** with no child pages: everything before its end-of-read marker, at 238 words a minute plus 30 seconds per diagram. Detail goes to the LLD, not to more HLD pages.
- **Keep counts and cross-references true.** Update the component, phase and section references you touch.
- **Open items live in the LLD gap register** (the "Gap register" appendix). Add a new `G-NN` row there; do not hold it in chat.
- **Pursued features update the docs in the same branch.** A proof of concept gets a file-map line and a gap row. Once a feature is adopted (dashboard, `init`, …), the HLD and LLD gain its component, verbs, config keys, file map and proof cases.

<!-- komodo:begin min=1.0.0-beta.6 -->
#### Komodo harness

- **You are the orchestrator.** Drive the harness only through `komodo <command>`; with no terminal, every reply is the JSON envelope.
- **Installed at:** `/Users/rad/.komodo/bin/komodo`.
- **Find your commands:** run `komodo --help --json` first. It lists every command, flag and result schema, and marks the terminal-only ones.
- **Not yours:** the terminal-only commands (`install`, `update`, `config set`). They refuse without a terminal. Hand the person the command.
- **Wait without polling:** run `komodo status --wait` in the background. It returns when a group is blocked or done, with the blocked group's question.
- **Answer a blocked group:** `komodo run <id> --approve` grants what it asked for; `komodo run <id> --deny` refuses it.
- **Report:** relay the envelope's `report` field verbatim.
# Komodo output style: adhd

Komodo prints replies in the `adhd` profile. Relay the `report` text of a reply unchanged.

- Line one is the verdict: what happened, in one sentence.
- Labels are bold, such as **Run id:**.
- Every value the harness prints sits in a code span.
- Data lists are tables of at most 5 rows with cells under 45 characters; the rest is one line, `<N> more: <what>`.
- A list holds at most 5 bullets, and a choice at most 3 options.
- When turn-end summaries are on, up to four closing headings follow, each skipped when empty, in this order: `✅ Successful Changes`, `❌ Blocked Changes`, `📌 Callouts`, `⚠️ Warnings`.
<!-- komodo:end -->
