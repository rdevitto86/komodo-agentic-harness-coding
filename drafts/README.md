# Brief drafts (S07, S11)

Samples to vet before S11 is approved. Nothing here ships; the real files land in `embed/agents/` in phase 1.

| File | What it shows |
|---|---|
| `agents/builder/prompt.md` | builder role text and brief template |
| `agents/builder/agent.json` | what Go reads: tools, limits, skills, result schema |
| `agents/builder/brief.example.md` | one filled brief, as the model sees it |
| `agents/reviewer/prompt.md` | reviewer role text and brief template |
| `agents/reviewer/agent.json` | reviewer tools and per-task verdict schema |

## Assembly order

Most stable first, so the provider's prompt cache reuses the longest identical start.

| # | Part | Changes when | Source |
|---|---|---|---|
| 1 | Tool definitions | the agent changes | `agent.json` `tools` |
| 2 | Role | the agent changes | `prompt.md` `# Role` |
| 3 | Repo context | the repo's base commit changes | `AGENTS.md` minus the Komodo block |
| 4 | Standards | the task's file types change | S09 skills, `always` first, then by name |
| 5 | Task | every task | the slots below |

No harness rules appear in any part. The guard (S12), the result schema (S08) and Check (S19) enforce them.

## Slots

`R` is required: empty fails the brief. `O` is optional: empty renders `none`.

| Slot | Source | Builder | Reviewer | Tester | Planner | Architect, researcher | Scout |
|---|---|---|---|---|---|---|---|
| `repo_context` | `AGENTS.md` at base, block stripped | O | O | O | O | O | — |
| `repo_profile` | S04 detect: languages, command classes | R | R | R | R | O | — |
| `standards` | S09 | O | O | O | — | O | — |
| `group` | backlog at base: id, title, goal | R | R | R | — | — | — |
| `tasks` | backlog at base: id, title, card, `done_when` per task | R | R | R | — | — | — |
| `files` | the group's `files` list | R | R | — | — | — | — |
| `file_contents` | each file in `files` at base; `new file` if absent | R | — | — | — | — | — |
| `diff` | base → builder head | O (repair) | R | O | — | — | — |
| `checks` | S19 results: command, exit code, failing blocks | O (repair) | R | O | — | — | — |
| `findings` | open reviewer findings | O (repair) | O (re-review) | — | — | — | — |
| `scope_answer` | the orchestrator's `orch scope` answer | O (resume) | — | — | — | — | — |
| `lens` | S20 lens text | — | R | — | — | — | — |
| `callers` | Go callers of changed symbols (`go/packages`) | — | O | — | — | — | — |
| `specs` | spec anchors and decisions the tasks cite | O | O | — | O | O | — |
| `goal` | the request | — | — | — | R | R | R |
| `layout` | repo tree, paths only | — | — | — | R | R | R |
| `backlog` | existing epics and groups | — | — | — | O | — | — |
| `repo_map` | S25: ranked symbol graph, fitted to 10% of the ceiling | — | O | — | R | R | — |
| `baseline` | S19: tests failing at the base commit | — | — | O | — | — | — |
| `lens_rules` | S20: the applicable lenses' rule catalogs | — | R | — | — | — | — |

20 slots. The last 3 were added in the 2026-10-08 design pass.

Merged from beta 5's 19 slot names: `task_id`, `title`, `task_block` and `done_when` become `tasks`; `failure` becomes `checks`; `repo_rules` becomes `repo_context`; `context` and `repo_context` (vague) become `specs` and `callers`; `base` moves into `group`; `existing` becomes `backlog`.

## Brief ceiling per link

A brief has no per-slot cap. Its only ceiling is half the link's context, measured before each session (S05), which leaves the other half for the session's own work. The table shows the usual numbers; the measured one wins.

| Link | Context | Brief ceiling |
|---|---|---|
| Claude Opus 5.5, Sonnet 5.5, Fable 5.1 | 1M | 500K |
| Claude Haiku 4.5 | 200K | 100K |
| OpenAI GPT-5.5, API | 1M | 500K |
| OpenAI GPT-5.5, Codex | 400K | 200K |
| Local floor: Qwen3.6-27B, Kimi K2.6 class | 262,144 | 131K |

An endpoint below the floor fails `doctor` and is skipped; briefs are designed for at least 131K tokens.

Over the ceiling, the brief goes to the next link in the chain that fits (S09); with none left, the task blocks with `context`. Nothing is cut to fit: a small local link takes only the briefs that fit it.
