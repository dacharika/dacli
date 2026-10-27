# Native hosts and skills

Use the native plugin in the selected host. Codex is measured; other hosts need actual conformance tests before support claims. No runtime adapter presets or provider fallbacks remain. Install skills through the host and keep private session credentials outside skill/plugin files. Skill prose and host model names do not grant authority.

See the [operator playbook](https://github.com/dacharika/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md) and inspect `dacli capabilities --json` for the installed surface.

Update the owning source, regenerate its supported package, and use the host's documented installation/selection flow. Do not edit installed plugin caches. The standalone skill is mirrored byte-for-byte in the binary's embedded guidance assets; keep those existing file sets synchronized. A new source bundle may require reviewed manual migration when the installed copy is not a recognized legacy bundle.
