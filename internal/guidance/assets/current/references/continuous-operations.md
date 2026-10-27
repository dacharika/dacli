# Repeated bounded work

Keep multiple independent feature lanes moving for a multi-feature request.
After a lane delivers or encounters a real blocker, refresh canonical readiness
and choose the next eligible feature within existing user authorization. Preserve
the critical lane while other disjoint features implement, review and land.
Do not wait for one leaf task to finish before considering unrelated ready work.

Use current session identity, exact claims and live lease protection for each
lane. Carry prior user authorization forward for authorized reversible actions;
routine next steps do not need repeated permission. If one session expires,
stop its writes and use an already-authorized owner renewal/recovery workflow or
report the precise missing authority while other valid lanes continue. A policy
refusal ends that unchanged request, not all unrelated work.

The native host owns repeated cycles and scheduling. Separately enabled bounded
controller tools govern recorded invocation progress; no resident provider
service, automatic host dispatcher or rolling token enforcement is implied.
Centralize expensive exact-candidate QA and keep per-feature review and merge
independent. Stop when authorized eligible work is absent; do not invent backlog.

See [parallel features and bounded swarms](swarms-loops.md) and the
[operator playbook](https://github.com/dacharika/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md).
