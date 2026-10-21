# komodo-agentic-harness-coding

Komodo is one Go program that turns a goal into reviewed, published code. It plans the work into task groups, then runs each group through a fixed pipeline of agents (builder, Quality Assurance, reviewer) inside a private copy of the repository. It publishes only work that passed the checks its risk level requires, and a person merges the result.

Two callers drive it: a person at a shell, and the orchestrator, a model chat session in Claude Code that turns the person's intent into commands. With no terminal, every reply is a JSON envelope.

## Design

The design is the source for every fact about how Komodo works.

| Document | Holds |
|---|---|
| [docs/hld.md](docs/hld.md) | Purpose, ownership, interactions and decisions; a 20-minute read |
| [docs/lld.md](docs/lld.md) | Types, interfaces, algorithms, limits, configuration keys and the gap register of open items |

`make docsite` serves both as a rendered site at `http://127.0.0.1:8765/hld/` [Makefile:9-10].

## Install

Requires Go 1.27 [go.mod:3].

```sh
make gate
```

`make gate` builds, vets and tests the code, builds it for Linux, macOS and Windows, then places the binary at `~/.komodo/bin/komodo` [Makefile:20-33]. Set `KOMODO_BIN` to place it elsewhere [Makefile:2].

Then install the harness into a repository from a terminal:

```sh
komodo install
```

It puts the binary on the machine and a Komodo block into the repository's `AGENTS.md`. Its flags include `--repo-only`, `--machine-only`, `--remove` and `--dry-run`.

## Commands

Run `komodo --help --json` for every command, flag and result schema. Komodo has 13 commands.

| Command | What it does |
|---|---|
| `install` | Install the harness for this machine and repository |
| `config get` | Print settings and the layer that set each |
| `config set` | Set, reset or accept settings |
| `login` | Set up model access for one provider |
| `update` | Update the binary to the newest signed release |
| `plan` | Plan task lists from a goal or documents |
| `run` | Start, schedule or continue a run, answer a blocked group, or override a spent one |
| `stop` | Stop a run or remove a queued run |
| `status` | Show live runs, blocked groups and queued runs |
| `replay` | Play back the recorded events of a run or group |
| `abandon` | Drop a task group for good |
| `clean` | Remove everything that belongs to no open run |
| `ui` | Serve the read-only web dashboard |
| `doctor` | Check the install, sandbox, Docker, model links and Komodo block |

`config get` and `config set` count as one command, so the table has 14 rows for 13 commands.

**Terminal-only commands:** `install`, `update`, `login` and `config set`. They refuse to run without a terminal, so the orchestrator hands the person the command.

A blocked group is answered with `komodo run <id> --approve` or `komodo run <id> --deny`. A group that spent its tries gets one final try with `komodo run <id> --override --minutes <n>`.

## Configuration

Settings are changed only through the Configuration Manager:

```sh
komodo config get
komodo config set key=value
```

`komodo config set --default <key>` resets one key, and `komodo config set --accept` accepts settings edited by hand. Built-in defaults live in `embed/defaults.json`; the LLD lists every key.

## Test

```sh
go test ./...
```

`make gate` runs the full set of checks, including the build and vet for the three operating systems. `make dashboard` serves the dashboard proof of concept on moving mock data.

## Layout

| Path | Holds |
|---|---|
| `cmd/komodo` | The program's entry point |
| `komodo.go`, `embed/` | Files compiled into the binary: defaults, agents, skills, templates, output text, schemas |
| `internal/cli`, `internal/format`, `internal/config` | Commands, reply text, settings |
| `internal/bus`, `internal/engine` | Event Bus and Runtime Manager |
| `internal/cache`, `internal/lease`, `internal/state` | Cache, lease and runtime state managers |
| `internal/intake`, `internal/contextmgr` | Planning and agent context |
| `internal/task`, `internal/agent`, `internal/llm`, `internal/sandbox` | Task Manager, agents, model client, sandboxes |
| `internal/commit`, `internal/cleanup` | Publishing and cleanup |
| `internal/fault`, `internal/metrics`, `internal/resource` | Error Handler, Metrics Service, Resource Manager |
| `internal/core`, `internal/integration` | Shared file and git helpers; integration tests |
| `dashboard/` | The read-only web dashboard |
| `docs/`, `docsite/` | The design and the tool that renders it |

[AGENTS.md](AGENTS.md) holds the rules for working in this repository.

## License

See [LICENSE](LICENSE).
