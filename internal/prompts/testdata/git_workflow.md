<!-- dacli-prompt schema: dacli-prompt/v1 base: native-governance/v1 -->
{{- /* Stack awareness (dacli 192). `.Stack` is the project's recorded toolchain;
it is absent on every project written before stacks were recorded, and this
template must be byte-identical to its pre-192 self in that case. The values are
hoisted into variables through a `with` + `.Recorded` guard because
text/template treats EVERY struct as true — testing `.Stack` alone would send a
zero Stack down the recorded branch and silently drop the advice entirely. */ -}}
{{- $label := ""}}{{$fmtcmd := ""}}{{$buildcmd := ""}}{{$testcmd := ""}}
{{- with .Stack}}{{if .Recorded}}{{$label = .Label}}{{$fmtcmd = .Format}}{{$buildcmd = .Build}}{{$testcmd = .Test}}{{end}}{{end}}
## Git discipline
You are working in a git repository. Never commit to the default branch — your work reaches the trunk through a branch and a PR, never a direct push.
- Before your first change: git checkout -b {{.Branch}}
{{- if $label}}
{{- if $fmtcmd}}
- Before you commit, format what you touched: run `{{$fmtcmd}}` over every {{$label}} source file you changed (test files included). CI rejects an unformatted file — an unformatted test is the most common way a green-locally change fails CI.
{{- end}}
{{- if $buildcmd}}
- This is a {{$label}} project: build it with `{{$buildcmd}}`. Do not reach for another language's toolchain.
{{- end}}
{{- else}}
- Before you commit Go code, format it: run `gofmt -w` on every `.go` file you touched (test files included). CI runs `gofmt -l .` and REJECTS an unformatted file — an un-gofmt'd test is the most common way a green-locally change fails CI.
{{- end}}
- Commit each logical change through dacli so the commit is attributed to YOU and your role — this is how the team tracks who implemented what, and how reviewers use blame to improve agents:
    {{.Exe}} commit "{{.Ref}}: <what changed>" --task {{.Ref}}
  (dacli sets the author to your agent id and role and stamps provenance trailers; do NOT use plain `git commit`, which would lose the attribution.)
- Stay inside your claim: edit only the files this task owns. A commit that reaches into a sibling's tree is how parallel work corrupts itself — if the task genuinely needs a path outside your scope, file a finding and say so; do not grab it.
- Run the project's test suite{{if $testcmd}} (`{{$testcmd}}`){{end}} before declaring any acceptance criterion met. A red suite means the box stays unchecked — no exceptions.
{{- if .PR}}
- If your assignment authorizes external publication, use the exact task-bound PR path:
    {{.Exe}} pr --task {{.Ref}}
  Inspect the projected body and repository authority first. A PR is not landing
  evidence; do not infer approval, merge or issue closure from its creation.
  Auto-merge and release publication require separate explicit authority.
{{- else}}
- Publication is not requested by this workflow. Report exact branch, commit,
  tree and retained verification evidence to the owner. Owner acceptance and
  integration retain their existing policy checks; do not close your own task.
{{- end}}
- Delegate only through the native host when the assignment allows it. Separate
  host agents sharing an MCP connection still share one enrolled identity;
  independent review requires eligible credentials and owner-issued grants.
- Work only in the assigned isolated checkout. A native attempt and fenced lease
  govern scoped operations through dacli; they do not enforce host filesystem access.
