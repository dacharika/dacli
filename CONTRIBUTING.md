# Contributing to dacli

dacli turns a repository into a multi-agent workspace. Its product is a
**record** — of what was decided, what was built, and what was verified — so
the most expensive bug it can have is a record that disagrees with reality.
Almost every convention below exists because that happened.

Humans and agents contribute through the same rules. If you are an agent, read
[AGENTS.md](AGENTS.md) as well; it covers the parts specific to working inside
a loop.

---

## Getting set up

```bash
git clone https://github.com/mlnomadpy/dacli && cd dacli
go build ./cmd/dacli
```

That works from a clean checkout — `ui/dist/.gitkeep` is tracked, so the
`go:embed all:ui/dist` in the dashboard slice always has a directory to read,
and `go test ./...` passes without Node installed at all. Manually dispatched
CI verifies this in the `clean-checkout` job.

What you get without the frontend build is the dashboard's fallback page rather
than the real SPA. Build it when you are working ON the dashboard:

```bash
cd internal/features/dashboard/ui
npm ci
npm run build
npm run test:coverage
```

Dashboard coverage includes every production TypeScript and Vue source file,
not only files imported by a test. Vitest enforces floors of 82% statements,
76% branches, 80% functions, and 84% lines. These are collapse detectors just
below the measured baseline, not a claim that uncovered code is unimportant.
Raise them when coverage improves; never lower them merely to make a change
green. The report is written to the ignored `ui/coverage/` directory.

Run everything CI runs, before you push:

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test ./...
```

The Go coverage gate runs in CI with `-race` and then checks the repository and
safety-critical package floors in `scripts/coverage-floor.sh`. The hosted
control-plane packages have their own floors so a strong local CLI suite cannot
hide an untested API, worker, migration, or tenant boundary. Reproduce it with:

```bash
go test -coverprofile=coverage.out ./...
./scripts/coverage-floor.sh coverage.out
```

`golangci-lint` is pinned in CI. Install the same version:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
```

The reachable-vulnerability scanner is pinned for the same reason and runs on
the isolated patched Go 1.26.10 security toolchain, including the crypto/x509
fix for GO-2026-6809. Run the reviewed pair with:

```bash
GOTOOLCHAIN=go1.26.10 go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
GOTOOLCHAIN=go1.26.10 govulncheck ./...
```

Before upgrading either tool, verify its declared Go version against the lint
job and update `.github/workflows/contract_test.go` with the reviewed pair.

It needs Go 1.25+, while this module targets 1.22 — that is why CI runs it in
its own job on its own toolchain. The 1.22 floor is a compatibility guarantee
and is not bumped for a lint upgrade.

---

## The verification bar

**"The tests pass" is not verification.** This codebase has shipped tests that
passed while measuring nothing, more than once:

- an invariant test that asserted *some* error came back — most commands fail on
  a missing positional first, so it never once exercised the thing it was named
  for. Tightened to require the error *name the flag*, it immediately failed on
  28 commands.
- a safety gate whose refusal branch was reachable by zero tests: changing
  `if requireVerify {` to `if false {` closed unlanded work with a green suite.
- a "streaming output" test that read the file *after* the function returned,
  which proves output arrives eventually and nothing about streaming.

So the bar is: **state the mutation.** Break the code your test covers, and show
the test going red.

```bash
# revert the guard, then:
go test ./internal/features/acceptance/ -run Unlanded
--- FAIL: TestAcceptOneRefusesUnlandedUnderRequireVerify
```

If you cannot construct a mutation that your test catches, the test does not
cover the behaviour — whatever its name says.

**Measure the premise before you file.** A task was once filed here claiming the
loop buffered its redirected output. It does not; Go writes `os.Stdout` straight
through. The premise was never reproduced, two agents spent a cycle satisfying
it, and the "fix" was unreachable code. Reproducing it took one command.

---

## Conventions that are load-bearing

### The exit-code contract

| code | meaning |
|---|---|
| `0` | ok |
| `2` | usage — the caller's mistake |
| `3` | refused by policy — **an answer; never retry it** |
| `4` | not found |
| `1` | everything else |

The `1` / `3` distinction is the one that matters: a supervisor that retries a
refusal enters a loop it cannot exit. `clikit.Usagef` and `clikit.Refusedf`
produce 2 and 3. `ExitCode` maps them with `errors.As`, so wrap with `%w`, not
`%v`, when an error passes through `fmt.Errorf`.

### Declare capabilities on the command, not in the handler

`clikit.Command` carries `JSON`, `Mutates` and `Usage`. The dispatcher enforces
all three.

This is not style. Every guard in this codebase that was applied *by convention*
at ~100 call sites has drifted: `Flags.Reject` reached 4 handlers out of 112,
and four verified grant bypasses shipped next to correctly-gated siblings. A
command that describes itself cannot drift.

### Feature slices never import each other

```
shared    clikit, team, ulid, mdstore, spm, prompts
entities  model, workspace, store, eventlog, agentid, brief
features  internal/features/* — one slice per capability, ISOLATED
app       internal/cli, internal/mcp
```

`internal/cli/arch_test.go` enforces the isolation. When two slices need the
same logic, it moves down into `entities` or `shared` — shared landing and verification rules belong below the feature slices.

### Status is folder position

A task's status is *where its file lives*, not a frontmatter field. The event
log is append-only. Read-only agents *propose*; the owner applies.

### Comments explain why

The most valuable documentation in this repo is the header comment on each file
and the reason attached to each guard. Name the defect that shaped the code —
"(dacli 329)", "(issue #421)" — so the next reader can find out whether the
constraint still holds. `ST1000` is disabled in the linter precisely so those
headers can stay.

---

## What gets a change rejected

- A test that passes against the unfixed code.
- A guard that reports success from a path where it did no work — a filter that
  matches nothing, a gate that returns `true` when its read failed, a count
  derived from unvalidated input.
- A refusal downgraded to a warning without saying why.
- Dead code left behind as a "fix" — an unreachable branch that reads as
  handled is worse than an absent one, because the next person believes it.
- A `--dry-run` that describes something other than what the real path does.

## What gets it merged quickly

- A failing test first, then the fix.
- The mutation stated in the PR body.
- One defect per PR, with the *why* in the commit message rather than the diff.
- Naming what you were unsure about.

---

## Filing issues

Use the templates. The reports that moved this project most all had the same
shape: the observed symptom, the suspected cause, and the manual step you had
to take. You do not have to be right about the cause — a wrong guess that names
the right area still saves time, and several have.

## Releases

Release authority stays with the maintainer. `dacli release train` governs a
checks-, review-, and artifact-gated source-to-target promotion PR; even with
recorded merge authority it never creates a tag or publishes a release.

Tag and GitHub-release publication is a separate, explicit maintainer action.
`dacli github release` with an exact project and tag may perform it only when
the maintainer chooses that command. Neither a native host session, a release
train nor a CI workflow grants itself that authority. Changes to
publication automation must therefore document the exact authority,
idempotency, rollback, and release-asset verification boundary rather than
treating promotion and publication as one step.

## Code of conduct

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).
