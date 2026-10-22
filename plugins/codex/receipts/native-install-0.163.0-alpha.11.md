# Isolated Codex 0.163.0-alpha.11 observation

The adjacent JSON is the unmodified output of the actual current-host install,
MCP transport, named verification, independent review and recovery smoke on
2026-10-22. It uses temporary HOME/CODEX_HOME directories, a synthetic Git
workspace and owner-enrolled profiles. It does not change the user's installed
plugin and requests no model turn or coding CLI execution.

- Smoke source commit: `014ca4727e308046e9f1c55a3d882abddb0cb122`.
- Smoke source tree: `70aeda5127533c5cafaf9b68c61d73ab8318d212`.
- Smoke SHA-256: `2b3029af0d97df7174571fa2b97fe3866d947aa22b4c5a42b1444e902a2551b0`.
- DACLI binary source: `6d4a44d2ac5b7c0a543b138ac6d4a4ee7ac08e3b`, Go 1.26.10, clean VCS metadata.
- Binary SHA-256: `1724d2533328b45085d604959b495b9d6398ab0e9af7d1f9fe845191556c65e1`.

The only source difference from that validated binary is the diagnostic smoke
script; no compiled Go code changed. The full local Go suite, including all 221
authority tests, passed on that binary's source tree. The current smoke discovers
13 ordinary worker/read tools, the configured 16-tool verification catalog and
the separately enrolled seven-tool reviewer catalog.

The crash case starts a real configured check and confirms its durable pending
operation. It kills only the exactly identified fixture verifier process, then
restarts the host. Status, Cancel and Request preserve the original operation as
`reconciliation_required`, with no evidence/interruption, unchanged authority
bytes and exactly one check execution. An owned private FIFO releases the orphan
check and cleanup is observed. Known interruption and completed receipts remain
unchanged. This is observation and controlled fixture cleanup, not fabricated
terminal authority or automatic recovery of arbitrary processes.

A disposable source-only semantic mutation changed pending lookup classification
to `interrupted`. The actual smoke failed at
`assert unknown['state'] == 'reconciliation_required'`; restored source passed.
The implementation checkout and its real profiles were never mutated. The first
actual attempt failed before any verifier signal because macOS truncates `ps`
comm output. The final source uses exact full argument vectors and retains the
PID, parent, process group, start identity and executable checks.

This receipt does not qualify actual model workers, a dependent native swarm
wave, automatic private child-profile delivery, hooks/containment, running-turn
cancellation, external publication or another harness. Those remain separately
tracked by core594 and the host-specific qualification issues. Shared MCP
connections still share one identity. CI stays manual only.
