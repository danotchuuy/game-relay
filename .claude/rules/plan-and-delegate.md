# Plan first, then delegate to the cheapest capable model

Goal: spend Opus tokens on design, security-sensitive code and review; hand everything
else to cheaper models, in parallel where possible.

## 1. Plan before doing

For any task bigger than a quick change:

1. Read only what you need to understand the task.
2. Write a short plan for the user: subtasks, each with the files it owns, what "done"
   looks like, and its model tier. Mark which run in parallel.
3. Skip the plan for trivial tasks (typos, one-line fixes, answering a question).

## 2. Pick the cheapest tier that can do each subtask

| Tier | Use for | Examples in this repo |
|------|---------|-----------------------|
| `haiku` | Mechanical work with no design decisions | README / `.env.example` / design-doc updates, adding a field through an existing pattern, CSS tweaks, renames, writing tests from a list of cases, running commands and summarizing output, searching the codebase |
| `sonnet` | Implementing a slice from a written contract | a store package change, API handlers, a frontend page, a Discord/YouTube/Twitch client |
| Opus (main session) | Design and judgment | contracts and migrations, URL parsers, access/permission rules, anything touching auth or the tenant boundary, final review and integration |

- Default down: if a subtask fits the Haiku row, use Haiku. Try the cheaper tier first and
  escalate only if the result is wrong.
- If the same subtask fails twice, take it over or bump the tier.

## 3. Contract first, then fan out

For multi-package features:

1. Write the shared contract yourself before spawning:
   - migration SQL;
   - model types;
   - interface/stub signatures;
   - shared frontend API types;
   - a section in `docs/design.md`.
2. Give each agent **exclusive ownership of a directory or file set**, so parallel agents
   never edit the same file. Stub anything a parallel agent needs to compile against.
3. Consumers declare their own small interfaces and test against fakes. They never call
   another package that's being rewritten concurrently.
4. Agents only run tests for the packages they own (`go test ./internal/<pkg>/`), not `./...`.

## 4. Write self-contained, compact prompts

Agents start with no context. Each prompt includes:

- **Contract:** the goal and the doc section to read (point to `docs/design.md`; don't
  paste it).
- **Scope:** the files the agent owns and the files it must not touch.
- **Acceptance:** exact commands, e.g. `go vet` + `go test -race` for its package, or
  `npm run build && npm run lint`.
- **Required test cases**, listed.
- **Report:** a short summary, any deviations from the contract, and the last lines of the
  test output. No file dumps.

## 5. When NOT to delegate

Do it inline when:

- **It's small:** it takes a few tool calls or touches one or two files. Spawning, reading
  and reviewing costs more than the work.
- **Context matters:** it needs context you already have loaded (a bug you just
  diagnosed).
- **It's security-critical or design-critical:** parsers for untrusted input, permission
  checks, cross-tenant scoping.

## 6. Verify and integrate (always Opus)

- Read each agent's report for deviations. Spot-check the risky code yourself:
  - locking;
  - SQL scoping;
  - DnD/index math;
  - CSS that might overflow;
  - anything the agent flagged.
- Run the whole build and test suite after integrating:
  `go vet ./... && go test -race ./...`, then `npm run build && npm run lint`.
- Extend the end-to-end test in `cmd/server/e2e_test.go` with the new flow, using the real
  store and real access rules.
- Run migrations against a **copy** of the local database (`VACUUM INTO`), never the live
  file.
- Don't report work as done until it is verified. Say what wasn't tested (e.g. "not tried in a
  browser").
