# Antigravity provider readiness

The optional local operator report can discover the Antigravity CLI (`agy`)
from an explicit executable selection, `PATH`, or its documented install path.
It may run the bounded `agy --version` diagnostic. Authentication remains
`unknown`; this adapter does not read an OS keyring, token profile, browser
cookie, or provider configuration, and it does not start `agy` or open a
browser.

The report does not establish a selected personal or business account, Gemini
Enterprise license, API billing, quota, provider availability, successful
model request, native host identity, adapter qualification, execution
architecture, or dacli dispatch authority. Antigravity App, IDE integrations,
and extensions are outside this CLI discovery adapter.

See [provider readiness](../../docs/provider-readiness.md), [Antigravity CLI
getting started](https://antigravity.google/docs/getting-started?tab=cli), and
[installation and authentication](https://antigravity.google/docs/install?tab=cli).
