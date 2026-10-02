# The prompt registry

Current source removes provider-driven launcher prompts and their execution
paths. Surviving brief/refusal/admin descriptions remain reviewable templates.
Use `dacli prompt list` for the current registry; the native plugin has its own
typed tool descriptions and no CLI escape.


**Status: implemented** (`internal/prompts`, `dacli prompt list|show`).

Every multi-sentence piece of agent-facing prose dacli emits lives in one place: template files under `internal/prompts/tpl/`, embedded at build time, overridable per workspace. This exists because a prompt buried in an `Fprintf` chain cannot be audited, diffed in a PR, or improved without recompiling — and for this tool, **prompts are load-bearing artifacts**. It is the same doctrine as adapters and shortcuts: prose is data, not code.

The registry schema is `dacli-prompt/v1` and its current semantic base version
is `native-governance/v1`. Every template starts
with a machine-readable declaration:

```markdown
<!-- dacli-prompt schema: dacli-prompt/v1 base: native-governance/v1 -->
```

The declaration is stripped before delivery. Historical invocation receipts may
record prompt hashes and old semantic versions; preserve their bytes for audit.
They do not imply that a removed launcher template is still delivered.

## The registry

`dacli prompt list` shows the current registry with overrides marked;
`dacli prompt show <name>` prints a resolved template. Brief headers retain the
data-not-instructions warning. MCP refusals retain the do-not-retry guidance.
Descriptions of administrative tools must match the surviving command catalog.

## Overrides, compatibility, and migration

A file named `.dacli/prompts/<name>.md` wins over the embedded default — nearest-wins, same as templates. Prompt tuning becomes a **workspace commit**: attributable, revertible, reviewable, and visible to `dacli taint` like any other content. Every override must begin with the schema declaration above. Missing declarations and versions other than the binary's current schema fail closed; dacli never silently combines old lifecycle prose with a new scheduler. To migrate an override, diff it against the new embedded template, reconcile semantic changes, update the declaration, run the focused prompt tests, and review the resolved prompt with `dacli prompt show <name>`.

A broken brief header must not remove the untrusted-content warning. Historical
launcher overrides no longer grant an execution path. Native host prompts and
approval settings remain host-owned; private credentials must never be embedded
in instructions or committed templates.

## The boundary, so it doesn't erode

**One-line refusal and usage messages stay in code.** They are the exit-code contract's surface: tested by exact string, versioned with the behavior they describe, and meaningless apart from it. The rule of thumb: if it teaches an agent how to behave, it's a prompt (file); if it reports what just happened, it's a message (code). The 49 `refusedf`/`usagef` one-liners are messages.

## Audit trail

Extracted 2026-07-21 from five sites: `protocolPreamble`'s nine-Fprintf chain (the complaint that triggered this), the supervise correction, the brief header, the MCP refusal text, and 16 inline tool-description literals. The extraction immediately paid once: `get_context`'s description had already drifted (it didn't mention the Lessons section added by P1) — fixed in the registry file, where the next drift will be a one-line diff instead of an archaeology dig.
