# AGENTS.md

Guidance for coding agents working in this repository. `CLAUDE.md` is a symlink
to this file, so Claude Code reads the same conventions.

## Commits

Write commit messages as [Conventional Commits](https://www.conventionalcommits.org):
`<type>: <summary>`, imperative mood, lowercase summary, no trailing period.

Types used here: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`,
`build`, `ci`, `chore`. Prefer `style` for formatting-only changes (gofmt) and
`chore` for housekeeping that touches no behavior.

The body is optional but valued: explain why the change is needed and what it
fixes, not what the diff already shows.

**Never add AI attribution trailers.** No `Co-Authored-By:` naming Claude, no
`Claude-Session:` line, no "Generated with Claude Code" footer — in commit
messages, PR descriptions, or code comments. The commit is authored by whoever
runs git.

## Verifying a change

`make check` runs exactly what CI runs: `go vet`, the test suite, and a gofmt
check. Run it before committing.

Some tests skip themselves when an external binary is missing —
`TestPgDumpIntegration` skips without `pg_dump`, and CI has it. A green local
run is not proof CI is green; check for `SKIP` when touching catalog views.
