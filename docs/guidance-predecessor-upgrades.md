# Reviewed guidance predecessor upgrades

The local owner refresh supports the exact thirteen-file bundle shipped at
`ce282fcb27f6ff7f41152cc4e7ab800900605e8b` and the embedded current bundle
reviewed at `aaa87caa1b04e0121a0ea3e3df83ef13edeba941`. The predecessor
manifest pins its complete digest and the current target digest. A mixed,
customized, missing, unsafe, or unrecognized managed bundle is preserved for
manual review. Files beside the thirteen managed paths are left alone.

Run `dacli native guidance diagnose --skill-root <absolute-root> --json`, then
have the owner run `dacli native guidance refresh-preview --config <owner>
--skill-root <absolute-root> --plan-out <new-private-plan> --json`. Review the
immutable plan before the explicit `dacli native guidance refresh-apply
--config <owner> --plan <private-plan> --json`. The plan binds the exact
before-bundle identity and digest, manifest digest, target bundle, all file
digests and identities, deterministic private backup paths, and selected
workspace, installation, and authority. Its owner seal is checked with fresh
owner authentication at effect boundaries. Credentials are not serialized.

Every managed source file receives a byte-exact private backup before its
replacement. A crash can leave a partial set. Replaying the same plan only
reports verified progress; it does not resume or overwrite remaining files.
Restore exact originals from verified backups and inspect the reported recovery
path before creating a new plan. An unchanged `capabilities.json` row is only
counted as replaced when its target identity changed and its exact backup is
present.

The existing `dacli-guidance/v1` diagnosis and receipt schema and legacy plan
API remain supported. Callback-only v1 writers and appliers refuse v2 plans;
older strict readers also refuse them. A source upgrade does not update an
installed skill, plugin, cached chat text, host connection, or loaded runtime.
Fresh process and cached connection state remain unknown after file replacement.
