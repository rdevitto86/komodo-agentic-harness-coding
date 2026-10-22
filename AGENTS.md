# komodo-agentic-factory-coding

Komodo's code assembly line: one static binary is the conveyor and devices, markdown is all a model reads, one guard is the only hook, a model is a machine mounted per host. `docs/prd.md` is the requirements, proven by command.

## Rules that hold here

- **Nothing outside `internal/mount/` names a host.** No vendor, tool, path, or flag anywhere else.
- **Models read markdown, never Go.** Rules, roles, skills, and policy are the only files a machine sees.
- **Each hook has one job,** one stage and a refusal limit.
- **The gate is local.** `komodo gate` runs pre-commit and pre-push, no model; nothing runs on GitHub.
- **No MCP in 1.0.** MCPs land in a later hot-swap pass.
- **No repo config is required;** the gate refuses only on no build check (decision 0006).
- **Standard library only.** Go, no dependencies; `bin/` is gitignored, built by `komodo gate --install`.
- **A pull request opens with `komodo pr create`,** its body following `.github/PULL_REQUEST_TEMPLATE.md`.
- **The prototype is history,** at tag `prototype-final`; nothing returns without a task.

## Commands

```bash
go run ./cmd/komodo gate     # the whole precheck
go run ./cmd/komodo lint     # after a backlog edit
go run ./cmd/komodo doctor   # drift and portability
```

## Architecture context
- Read this repo's `docs/README.md` first; it indexes the PRD, HLD, LLD and decisions.
- For other services and cross-domain context, use the architecture portal (repo `komodo-architecture`; hosted URL pending).
- This repo's portal ID is `tool:agentic-harness-coding`. Its dependencies are in each doc's `depends-on` front matter.
- Names, ports and status come from `komodo-architecture/service-registry.md`.
