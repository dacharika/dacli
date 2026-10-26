# Operating profiles

Resolve the mode through `dacli start` so selection, routing, budgets,
verification, landing, and recovery are visible as one policy:

The orchestrator agent selects the smallest profile that can deliver the
product outcome and judges its results. dacli resolves and enforces the profile;
worker coding CLIs execute its bounded tasks. Human intervention is reserved
for authority, exceptions, emergency stop, and release policy.

```bash
dacli start --project <project> --profile inspect --dry-run
dacli start --project <project> --profile task --dry-run
dacli start --project <project> --profile wave --width 3 --dry-run
dacli start --project <project> --profile loop --dry-run
dacli start --project <project> --profile service --dry-run
```

Use inspect for a read-only diagnosis; task for one bounded edit; wave for
independent ready tasks; loop for repeatable backlog work; and service only for
a resident single-project runner. `--configure` persists the resolution without
execution, while `--show --json` reads the persisted policy. Service supervises
repeated finite loops with a lease, STOP file, rolling budget, unknown-landing
halt, and infrastructure breaker. It is not a multi-repo control plane and it
does not enable releases.

For a manual wave, use `next --project <project> --parallel <width>`, worktrees
with disjoint claims, `wait`, `sync`, verification, and the project's landing
policy. Set WIP no higher than the number of safe claims, reviewers, and landing
capacity.

A wave width is not a total spend cap. Preview every spawn with `--advise`,
require the reported token ceiling to be `ENFORCED`, and choose per-spawn
`--max-tokens` values whose sum stays inside the wave budget. If the operator
needs a durable rolling total rather than a one-wave allocation, use a bounded
loop with `--window-tokens` and `--token-window`. Advisory usage is accounting,
not enforcement.

Launch each ready writer detached so the wave can overlap, then follow its returned run ID rather than an unqualified log stream:

```bash
dacli spawn --task <ref> --role <role> --worktree --claim <path> --max-tokens <n> --detach
dacli agents --tail
dacli logs <run-id> -f
dacli wait
dacli sync
```

For a loop, always preview first: `dacli loop --project <project> --width N --max-cycles N --dry-run`. The real run keeps a finite cycle count, per-cycle `--max-tokens`, rolling `--window-tokens`/`--token-window`, `--halt-after-idle`, and an explicit `--idle` backoff. `--yolo` is a deliberate override, never a synonym for unlimited spend or release publication.
