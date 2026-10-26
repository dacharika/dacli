# Independent operator transport conformance

`internal/operatorconformance` is a Go consumer with its own wire structs;
`scripts/operator-conformance/client.py` is a second consumer built only on
Python's standard library. Neither imports `contracts/operator/v1` or
`internal/operatorread`. Both decode the same public fixtures under
`contracts/operator/v1/testdata` and speak JSON lines to an owned
`dacli operator serve` child through stdin/stdout pipes. The connection path is
only passed as an argument to that child. The consumers do not open it, read an
authority or credential file, invoke a terminal command inside the protocol,
or inspect stderr.

Both response decoders enforce the closed success/failure envelope, request ID,
protocol version, exact request scope, required RFC3339Nano observation times,
source identity, stable error shape and page metadata. Producer-owned `data`
remains opaque JSON. Request frames are capped at 64 KiB and response frames at
1 MiB; request/response and child shutdown waits are bounded. Fake-child checks
allow 20 seconds for a response (the producer deadline is 15 seconds) and 2
seconds for graceful child shutdown before termination. Fake-child checks cover
silence, a missing newline, oversize output and shutdown timeout.

## Fixture and independent decoder checks

From the repository root:

```sh
GOTOOLCHAIN=go1.26.10 go test ./internal/operatorconformance
python3 scripts/operator-conformance/client.py --self-test
```

The Go and Python consumers enumerate the same fixture directory. The tests
bind each response to the fixture request ID, validate the version and scope,
preserve stable failure codes, and confirm the stale timeline still labels its
historical check stale. Fixture results are labeled fixture-only; they are not
live authorization or freshness observations.

## Live selected transport

`scripts/operator-conformance/run.py` creates an isolated temporary Git/dacli
workspace, bootstraps only a disposable native authority, and creates a
separate `operator.read` reader for each consumer plus ordinary `read` profiles
that lack `operator.read`. It seeds three synthetic tasks and two dependency
events so activity pagination has a real second page. The owner harness may
read those disposable connection records to obtain public workspace and session metadata. It never
uses the live owner configuration or authority. The consumers receive only the
opaque selected connection path and public scope values.

```sh
python3 scripts/operator-conformance/run.py \
  --binary /Users/tahabsn/.config/dacli/bin/dacli-f1068df1-vcs \
  --go /Users/tahabsn/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.10.darwin-arm64/bin/go \
  --go-cache /private/tmp/dacli-next-20261023/go-cache \
  --output-dir /private/tmp/operator-conformance-receipts
```

The live path checks an authorized task read, a `governance.snapshot`, a v999
request receiving the stable `unsupported_version` failure, activity cursor
continuation, and a fresh child reconnect. Both consumers inspect only the
public snapshot facts and require schema 10 to report `budget`, `policy`, and
`controller` as `unsupported`, with spend state, provenance, and enforcement
explicitly `unavailable`. This checks runtime service capability preservation
separately from authorization. A separately enrolled ordinary `read` profile
then receives `denied` because it lacks `operator.read`; the receipt labels
that `missing_operator_authority`. The harness revokes the Python
`operator.read` session only within the disposable authority and verifies that
the same child and a reconnected child both receive `denied`. It removes the
disposable workspace and authority on completion; it retains only the mode-0600
receipt and consumer logs under the requested output directory.

### Same-process revocation

The owner harness coordinates revocation with a private FIFO. The Python
consumer writes a structured readiness receipt after its baseline read and
waits on the FIFO. The owner harness revokes that disposable session, signals
the FIFO, and the consumer sends the next request over the same child. The FIFO
carries no authority; the consumer never reads the revocation mechanism's
private state.

## Mutations and receipts

The named consumer guards have focused tests, and each was mutated once. These
are the observed red assertions; restoring the guards made the focused tests
green again:

- `request_id`: mismatched-response rejection in
  `TestConsumerRejectsMismatchedResponseIdentity`.
- `version`: response-version and scope rejection in
  `TestConsumerRejectsMismatchedVersionAndScope`.
- `capability`: `TestConsumerDoesNotTreatMissingCapabilityAsSuccess` and the
  Python `require_failure` self-test.

| Guard | Go mutation failure | Python mutation failure |
|---|---|---|
| `request_id` | `mismatched response id accepted: <nil>` | `consumer accepted mutated id guard` |
| `version` | `wrong response version accepted: <nil>` | `consumer accepted mutated version guard` |
| `capability denial` | `missing-capability success bypassed the expected denial gate` | `consumer accepted success where missing capability must deny` |

For each mutation, remove only the named check in the new consumer, run its
focused test and record the failing assertion, then restore the check and rerun
the focused test green. Do not mutate the server's unsupported-version guard.

For the qualification receipt, record the clean source commit/tree, the Go and
Python runtime versions, the selected executable path and SHA-256, the SHA-256
of every fixture used, and the output/exit status for each consumer. Label each
observation `fixture` or `live` and include the selected local transport
identity for live cases. Keep private connection and credential contents out
of the receipt. Native Mac checks are local; hosted CI dispatch is manual only.
