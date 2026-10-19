# <Repository name>

<!-- One or two sentences: what this repository is for and who uses it. -->

This file tells an agent what it would otherwise have to guess. Keep it short; delete a section the repository has no use for.

## Commands

<!-- A wrong command is worse than a missing one. Delete a row the repository has no equivalent for. -->

| Do | Run |
|---|---|
| Verify (everything that must pass before a push) | `<command>` |
| Test | `<command>` |
| Build | `<command>` |
| Run locally | `<command>` |
| Lint | `<command>` |

## Where things are

<!-- Only paths that are not obvious from the language's standard layout. -->

## Gotchas

<!-- Behaviour that has already cost someone a debugging session. One line each. -->

## Deviations

<!-- Where this repository departs from a standard, and which one. -->

## Working rules

### Evidence

- Never guess. Read the code, run the command or look it up; mark what you did not check.
- Cite the source of every claim: a file and line, a command, or a link.
- A fix starts with a failing test or a repro command. "Done" means it now passes, with the output shown.
- When something recurs, find the root cause before adding a guard or a workaround.
- Re-read a file before citing it, and check a note against the code before acting on it.

### Scope

- A question, a review or a discussion approves no edit. Report findings only.
- An execution verb ("fix", "implement", "build", "refactor", "go ahead") approves every edit inside the named target.
- Ask first before a cloud infrastructure change, a database schema change, real-money spend, a delete outside the named scope, and anything that touches the main branch.
- Answer every question in the prompt before you implement anything.
- Deliver the artifact that was asked for, not a substitute. Complete every part of a multi-part request, and list any part left undone.
- Add nothing that was not requested: no extra scripts, guards or helpers. Propose extras instead.
- Never re-add what the owner removed, and never remove the owner's own comments or text.
- Once execution is approved, finish it.

### Comments

- Comment every function, public or private, in at most 3 lines. Test files and test helpers may go without.
- Comment a type, constant, variable or field in at most 1 line.
- Say what the code does; never restate the name.
- A comment describes the code only: no plan, task, ticket, phase, session or version reference.
- Never open a comment with a label such as `WHY:`. A banner comment uses only `// ---` and a short description.
- Never remove or rewrite a comment you did not write.

### Errors

- An error string describes the failure and never starts with the function name: `unmarshal credentials: %w`, not `GetUserCredentials: unmarshal: %w`. Function names belong in log metadata or stack traces.

### Git and pull requests

- Commit and push on a non-main branch freely. Never write to the main branch.
- Open every pull request from the repository's pull request template, with labels.
- Add no tool attribution anywhere: no co-author trailer and no "generated with" line.
- After you commit finished work on a branch, open the pull request. Never merge it; merging is the owner's.
- A changelog has no `[Unreleased]` section. A backlog or to-do file has no checkboxes: delete a finished item, since git history and the changelog record it.
