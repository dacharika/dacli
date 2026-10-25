# Local operator read protocol v1

This directory is the portable JSON frame contract proposed for issue #1146.
It is not a transport adapter and does not authenticate callers, read workspace
state, or grant authority. `contract.go` strictly decodes request/success/error
frames, rejects unknown fields and trailing JSON, and validates protocol
version, operation shape, limits, and timestamps. The `data` payload is owned by
the named canonical producer; adding a second projection model is out of scope.

The checked-in JSON Schema documents the closed frame envelope. Golden files in
`testdata` pin stable resource IDs, source identity, observation times, and
redaction examples. Their `harness` objects are test metadata only and are never
wire frames. Run `go test ./contracts/operator/v1` with Go alone.

A valid frame is structural conformance only. It does not establish local
identity, scope authorization, freshness, or evidence validity. Those claims
belong to the authenticated Go producer boundary in a future implementation.
