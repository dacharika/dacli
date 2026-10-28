#!/usr/bin/env python3
"""Static source audit generator. Does not execute dacli or assert runtime coverage."""
import argparse, collections, json, pathlib, re
HERE = pathlib.Path(__file__).resolve().parent
REV = 'cd7210a02637696c3b082e2eb7e113101565d457'
p = argparse.ArgumentParser()
p.add_argument('--source', type=pathlib.Path, required=True)
a = p.parse_args()
root = a.source
import subprocess
assert subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD'],text=True).strip() == REV, 'source revision differs from frozen ledger revision'
assert subprocess.run(['git','-C',str(root),'diff','--quiet','HEAD','--','AGENTS.md','CONTRIBUTING.md','docs/TRUST.md','internal/cli','internal/clikit','internal/features']).returncode == 0, 'audited tracked source differs from frozen revision'

def lines(path):
    return (root / path).read_text().splitlines()

def source(path, anchor, span=1):
    matches = [i + 1 for i, line in enumerate(lines(path)) if anchor in line]
    if not matches:
        raise ValueError(f'missing source anchor {path}: {anchor}')
    return {'path': path, 'startLine': matches[0], 'endLine': matches[0] + span - 1}

# Clauses are atomized by observable obligation, not by Markdown headings.
# Owners below are proposed verification roles, not authenticated approvals.
RULES = r'''
AG001|AGENTS.md|Read|preserve|integration-owner|Apply CONTRIBUTING verification, exit contract and isolation equally to agents.|Review each agent slice against CON rules before integration.
AG002|AGENTS.md|It decides what you may write|redesign|identity-policy-owner|Authenticate a credential before deriving any write capability; never infer native authority from a host session.|Test missing, empty and unverified credentials cannot authorize mutation.
AG003|AGENTS.md|A read-only|redesign|event-store-owner|RO may append attributed proposals; only authorized owner materializes state.|Test proposal append is distinct from state apply and owner checks run on apply.
AG004|AGENTS.md|Claim only|preserve|admission-owner|Admit only declared touched paths; refuse overlap and narrow scope instead of bypass.|Test conflicting live claims refuse and disjoint claims admit.
AG005|AGENTS.md|A `3` is final|preserve|supervisor-owner|Policy refusal terminates unchanged retries; report a finding if no permitted route remains.|Test supervisor does not retry typed refused result.
AG006|AGENTS.md|Edit paths relative|preserve|worktree-owner|Edits and verification must use the agent's own worktree, never accidentally main checkout.|Test workspace identity is tied to checkout and evidence revision.
AG007|AGENTS.md|The workspace is shared|redesign|workspace-store-owner|Linked worktrees share one canonical workspace; never initialize a competing store.|Test linked checkout resolves canonical authority and refuses duplicate initialization.
AG008|AGENTS.md|Commit with|redesign|vcs-adapter-owner|Governed commits enforce claims and carry attributable agent, role and task metadata.|Test commit port refuses unclaimed paths and records verifiable provenance; do not promise old CLI compatibility.
AG009|AGENTS.md|Run the thing|preserve|verification-owner|Observe execution before claiming behavior or filing a reproduced defect.|Evidence review requires actual command, revision, result and limits.
AG010|AGENTS.md|after a pipe|preserve|verification-owner|Capture the actual governed command exit code, not a downstream pipe's status.|Test wrapper retains child exit status when output is transformed.
AG011|AGENTS.md|When you add a test|preserve|verification-owner|State causal mutation, observe named red failure, restore green and record failure line.|Independent reviewer checks exact mutation and named failure receipt.
AG012|AGENTS.md|If you could not verify|preserve|verification-owner|Record unverified claims explicitly in task evidence.|Evidence schema distinguishes unverified, failed and passed.
AG013|AGENTS.md|Acceptance criteria must|preserve|planning-owner|Acceptance must be independently checkable with command/file/observable state.|Review rejects subjective or self-only acceptance checks.
AG014|AGENTS.md|Lead with the verb|redesign|routing-owner|Declared task intent and routing must agree; do not silently route implementation to review.|Test explicit intent routing and ambiguity refusal; migration of title heuristics requires decision.
AG015|AGENTS.md|Check for a duplicate|preserve|planning-owner|Inspect open work before filing duplicate tasks.|Workflow review records duplicate query or its unavailability.
AG016|AGENTS.md|One task, one sitting|preserve|planning-owner|Decompose work too large for a bounded sitting.|Review task scope and independent acceptance before dispatch.
AG017|AGENTS.md|Use `dacli report`|redesign|disclosure-owner|Tool reporting attaches diagnostic context but withholds workspace/transcript unless explicitly disclosed.|Test report export redaction and exact disclosure authority.
AG018|AGENTS.md|Report the|preserve|planning-owner|Reports distinguish observed symptom, suspected cause and manual workaround.|Template validator and reviewer require the three fields.
AG019|AGENTS.md|Inventing work|preserve|planning-owner|Do not invent evidence-free work; an empty cycle may produce no task.|Review separates measured defect from speculation.
AG020|AGENTS.md|Broadening scope|preserve|admission-owner|Finish assigned scope; record unrelated work separately.|Review diff against admitted claim; reject scope drift.
AG021|AGENTS.md|Deleting a failing test|preserve|verification-owner|Do not remove failing tests or weaken correct assertions; explain genuinely wrong tests.|Review test diff and mutation receipt.
AG022|AGENTS.md|Reformatting or renaming|preserve|admission-owner|Do not reformat or rename outside the claimed slice.|Claim-aware diff check includes renames and formatting-only changes.
AG023|AGENTS.md|Auto-fixers on fixtures|preserve|verification-owner|Review auto-fixer changes, preserving intentionally invalid fixture semantics.|Test invalid fixture still reaches intended rejection branch.
AG024|AGENTS.md|gofmt -l|redesign|toolchain-owner|Run required applicable gates before completion; keep legacy Go gates for Go changes and define Rust gates separately.|Record exact scoped gates; no claim this static audit ran Go verification.
AG025|AGENTS.md|Then mark your acceptance|preserve|acceptance-owner|Only mark criteria actually satisfied; leave acceptance to owner and do not self-close another's task.|Test unverified criteria and non-owner closure refuse.
CON001|CONTRIBUTING.md|record that disagrees|preserve|evidence-owner|Recorded state and verification claims must agree with actual observations.|Test failed/unknown effects cannot be recorded as completed success.
CON002|CONTRIBUTING.md|clean checkout|redesign|toolchain-owner|A clean checkout builds the core without requiring an unrelated UI toolchain; absent UI is reported honestly.|Rust clean-checkout build must not require Node or Swift; legacy dashboard fallback remains Go-owned.
CON003|CONTRIBUTING.md|Build it when|redesign|dashboard-owner|Dashboard changes run frontend install, build and coverage checks.|Legacy UI checks retained only for affected dashboard; define native UI checks independently.
CON004|CONTRIBUTING.md|coverage includes every|preserve|dashboard-owner|Coverage includes all production sources, not just test imports.|Coverage configuration audit checks source inclusion.
CON005|CONTRIBUTING.md|never lower them|preserve|verification-owner|Coverage floors are collapse detectors and must not be lowered merely to pass.|Review threshold changes against measured baseline; legacy floors are82% statements,76% branches,80% functions,84% lines and remain Go-dashboard-specific.
CON006|CONTRIBUTING.md|Run everything CI|redesign|toolchain-owner|Run formatter, static checks, lint and tests before push.|Define Rust equivalents without pretending cargo alone replaces Go or UI gates.
CON007|CONTRIBUTING.md|with `-race`|redesign|verification-owner|Safety and tenant boundaries need independent floors and concurrency checks; aggregate coverage cannot conceal them.|Choose Rust concurrency and boundary gates; preserve existing Go race/coverage checks for Go adapters.
CON008|CONTRIBUTING.md|pinned in CI|redesign|toolchain-owner|Pin lint tools and use the reviewed CI version locally.|Rust toolchain/lint versions recorded in CI; Go pin remains v2.12.2 for legacy source.
CON009|CONTRIBUTING.md|scanner is pinned|redesign|security-owner|Pin vulnerability scanner and isolated reviewed toolchain; upgrades update workflow contract.|Review Rust scanner/version policy separately; retain Go v1.8.0 and Go1.26.10 reviewed pair for legacy scans.
CON010|CONTRIBUTING.md|verify its declared Go|preserve|toolchain-owner|Check tool declared language version against CI before upgrade.|Toolchain update includes compatibility evidence and contract change.
CON011|CONTRIBUTING.md|1.22 floor|requires-human-decision|maintainer|Go1.22 compatibility is retained for legacy module; select explicit Rust MSRV rather than inheriting a Go floor.|Maintainer records Rust MSRV/support policy before publishing compatibility promise.
CON012|CONTRIBUTING.md|state the mutation|preserve|verification-owner|A behavioral test must detect the causal guard/effect mutation, not merely any error.|Run named mutation red and restored green; keep evidence revision.
CON013|CONTRIBUTING.md|streaming output|preserve|verification-owner|Tests observe the property at the time it matters, including streaming before completion.|Test output arrives while producer remains running; reject eventual-only substitute.
CON014|CONTRIBUTING.md|Measure the premise|preserve|verification-owner|Reproduce the premise before filing a defect or claiming a fix.|Issue evidence distinguishes historical source incident from newly observed behavior.
CON015|CONTRIBUTING.md|code |preserve|kernel-owner|Preserve exit meanings: 0 success,2 usage,3 refusal,4 not-found,1 other; CLI and in-process typed result agree.|Semantic exit mapping tests, including wrapped errors and unknown failures.
CON016|CONTRIBUTING.md|never retry it|preserve|supervisor-owner|Do not retry an unchanged policy refusal.|Supervisor integration test asserts one attempt and explicit refused state.
CON017|CONTRIBUTING.md|wrap with `%w`|redesign|kernel-owner|Preserve typed causes across error context; do not flatten refusal to generic failure.|Rust structured error chain test maps refusal through added context.
CON018|CONTRIBUTING.md|carries `JSON`|redesign|kernel-owner|One action declaration supplies output capability, effect and usage; shared dispatcher enforces them.|Generate help/JSON/effect catalog from ActionSpec and reject unknown action/effect.
CON019|CONTRIBUTING.md|Feature slices never|preserve|architecture-owner|Feature slices do not import siblings.|Dependency/architecture check rejects cross-feature edges.
CON020|CONTRIBUTING.md|moves down into|preserve|architecture-owner|Shared landing/verification policy lives below feature composition.|Kernel crate dependency graph has no process/store/UI dependency.
CON021|CONTRIBUTING.md|status is|redesign|legacy-adapter-owner|Legacy Go task status is folder position; a new store must translate explicitly rather than infer frontmatter authority.|Adapter fixtures test folder authority; Rust canonical schema requires separate approved design.
CON022|CONTRIBUTING.md|log is append-only|redesign|event-store-owner|Record append-only facts and separate RO proposals from owner apply.|Store invariants account for legacy sync applied-field exception without importing it as new canonical mutation.
CON023|CONTRIBUTING.md|Comments explain why|preserve|review-owner|Explain guard rationale and incident reference, not merely mechanism.|Review causal guard changes for retained rationale/source reference.
CON024|CONTRIBUTING.md|`ST1000` is disabled|retire|toolchain-owner|Do not copy the Go-specific ST1000 lint exception into Rust; preserve its rationale in CON023.|Rust lint configuration has no fabricated Go lint compatibility setting.
CON025|CONTRIBUTING.md|passes against the unfixed|preserve|verification-owner|Reject regression tests that pass unchanged broken code.|Named causal mutation must make the intended assertion fail.
CON026|CONTRIBUTING.md|did no work|preserve|kernel-owner|Never report success for no effect, failed read or unvalidated count.|Semantic tests cover empty match, unknown read and invalid input without success claims.
CON027|CONTRIBUTING.md|downgraded to a warning|preserve|kernel-owner|A refusal cannot become warning-success without explicit reviewed rationale and truthful partial outcome.|Test coordinator preserves refusal/partial result rather than losing typed cause.
CON028|CONTRIBUTING.md|Dead code left|preserve|verification-owner|Do not claim unreachable code as a fix.|Test reaches changed branch with causal old behavior red.
CON029|CONTRIBUTING.md|describes something other|preserve|kernel-owner|Preview uses the same plan as apply and declares its own effect-free shape.|Test preview/apply plan equality and parser-valued dry-run confusion.
CON030|CONTRIBUTING.md|failing test first|preserve|verification-owner|For a behavioral fix, establish failing regression before restoration.|Receipt includes named initial failure and final result.
CON031|CONTRIBUTING.md|mutation stated in|preserve|review-owner|PR body states causal mutation.|Review record links mutation to test and revision.
CON032|CONTRIBUTING.md|One defect per PR|preserve|review-owner|Keep PR scope bounded and explain why in commit message.|Review changed paths and purpose against one accepted slice.
CON033|CONTRIBUTING.md|Naming what you were unsure|preserve|evidence-owner|State uncertainty instead of promoting guesses to verified claims.|Evidence explicitly lists unknown/unverified limits.
CON034|CONTRIBUTING.md|Use the templates|preserve|planning-owner|Use issue templates with observed symptom, suspected cause and workaround.|Issue review checks all three and reproduction status.
CON035|CONTRIBUTING.md|authority stays|preserve|release-owner|Maintainer retains release authority.|Test publication needs explicit exact authority, not inherited integration permission.
CON036|CONTRIBUTING.md|never creates a tag|preserve|release-owner|Release-train promotion and publishing/tag creation are separate capabilities.|Test promotion cannot invoke publication even with merge authority.
CON037|CONTRIBUTING.md|exact project and tag|preserve|release-owner|Publication requires explicit maintainer choice for exact project and tag.|Test mismatched project/tag and implicit choice refuse.
CON038|CONTRIBUTING.md|grants itself|preserve|identity-policy-owner|Host session, workflow and release train cannot mint their own publication authority.|Test no ambient CI/host privilege escalation.
CON039|CONTRIBUTING.md|publication automation|preserve|release-owner|Publication automation documents authority, idempotency, rollback and asset verification.|Review each external action's explicit policy and recovery evidence.
CON040|CONTRIBUTING.md|Code of Conduct|preserve|maintainer|Follow the Code of Conduct as a human contribution obligation; do not claim a kernel enforces conduct.|Maintainer/reviewer process owns conduct review.
K001|internal/clikit/mutation_authorization.go|func AuthorizeCommandMutation|redesign|kernel-owner|Unknown action/effect and unverified identity fail closed; legacy absent-token bootstrap fallback is not new Rust authority.|Core and CLI tests refuse unavailable mutation with missing/empty/nonempty unverified credential.
K002|internal/clikit/clikit.go|EffectFreeDryRun|preserve|kernel-owner|Preview bypass is opt-in metadata and parsed shape, never presence of an arbitrary argv token.|Test value-taking argument named --dry-run does not bypass policy.
K003|internal/cli/cli.go|func hasHelp|preserve|kernel-owner|Help returns metadata without performing a handler effect.|Test help does not invoke execution or require a side effect.
K004|internal/cli/cli.go|refuseUnsupportedJSON|preserve|kernel-owner|Unsupported JSON refuses explicitly; supported JSON is machine-readable output.|Test JSON parse and exact typed unsupported-output result.
K005|internal/clikit/mutation_authorization.go|LookupEnv|preserve|identity-policy-owner|Present-empty/unresolved inherited identity refuses before bootstrap or preview.|Test empty/invalid/foreign identity refuses even outside a workspace and under dry-run.
K006|internal/cli/cli.go|nativegovernance.AuthenticatesExplicitly|redesign|native-adapter-owner|Explicit native/device/role/exact-GitHub auth routes are independent, not legacy grant bypass authority to copy.|Future adapter authenticates independently; no Go credential reuse in first Rust slice.
K007|internal/cli/cli.go|ReadOnlyWhen|preserve|kernel-owner|Mixed read/write commands authorize the exact declared parsed invocation.|Test project show configuration flags and reconcile apply-safe remain mutations.
K008|docs/TRUST.md|sandbox. Shared MCP|preserve|host-adapter-owner|Workspace policy is not OS sandboxing; process containment belongs to explicit host adapter.|Review records host confinement separately from path/grant checks.
K009|docs/TRUST.md|SafeSegment|preserve|path-policy-owner|Contain user paths and terminate Git options; caller-controlled names cannot escape workspace or become options.|Port equivalent traversal and option-injection fixtures before enabling filesystem/VCS effects.
K010|docs/TRUST.md|lower bound|preserve|provenance-owner|Self-declared taint/provenance is a lower bound, not injection prevention or authenticity proof.|UI/export schema labels provenance limits and untrusted agent text.
K011|docs/TRUST.md|hash|preserve|identity-policy-owner|Never publish reusable credentials; expose bounded public IDs and redacted diagnostic metadata.|Test error/JSON/log outputs do not reveal supplied credential values.
K012|internal/cli/capability_invariant_test.go|It does NOT|redesign|event-store-owner|Legacy Mutates=false includes effectful proposal/event paths; model query, proposal append and authoritative mutation distinctly.|Complete effect inventory beyond these156 before claiming all writes governed.
K013|internal/cli/trust_test.go|func Test|redesign|inventory-owner|A trust-path mention is not semantic verification of effects, gates and undo.|Validate exact catalog membership and independently review effect contracts.
K014|internal/cli/cli.go|func declaredMutationInvocation|redesign|kernel-owner|One typed registry replaces manually maintained secondary capability lists and invocation exceptions.|Generated catalog/help/result consistency test uses the same ActionSpec entries.
K015|AGENTS.md|A checked box is a claim|preserve|inventory-owner|Unresolved decisions or unverified implementation must block an all-mistakes-covered assertion.|Machine checker refuses readiness while unresolved or planned rows remain.
'''
rows=[]
for line in RULES.strip().splitlines():
    ident,path,anchor,disposition,owner,req,check=line.split('|')
    rows.append({'id':ident,'source':source(path,anchor),'requirement':req,'disposition':disposition,'verificationOwner':owner,'check':check,'implementationStatus':'planned','decisionStatus':'unresolved' if disposition=='requires-human-decision' else 'proposed','evidenceStatus':'source-audited; not runtime-reproduced'})

snapshot=json.loads((HERE/'command-literals.snapshot.json').read_text())
commands=[dict(x) for x in snapshot['rows'] if x['fields'].get('Mutates')=='true']
for family,line,path in [('external',22,'internal/features/nativegovernance/external.go'),('integration',23,'internal/features/nativegovernance/integration.go')]:
    for op in ['upgrade','configure','apply','reconcile']:
        commands.append({'path':f'native {family} {op}','source':path,'line':line,'factoryExpansion':True,'fields':{'Path':f'"native {family} {op}"','Mutates':'true','JSON':'false' if op=='upgrade' else 'true','Brief':'Owner-only durable '+('optional GitHub recovery' if family=='external' else 'local fast-forward integration'),'Run':f'{family}Command(ctx,args,"{op}")'}})
cap=(root/'internal/cli/capability_invariant_test.go').read_text().split('wantMutating := map[string]bool{',1)[1].split('\n\t}',1)[0]
expected=set(re.findall(r'"([^"]+)"\s*:\s*true',cap))
actual={x['path'] for x in commands}
assert len(commands)==len(actual)==156 and actual==expected, (len(commands),sorted(expected-actual), sorted(actual-expected))
trust=lines('docs/TRUST.md')
preview=set('ship;cleanup;branches prune;skill compile;release train;events reconcile;run;github push;github sync;github pull;task acceptance migrate;github project;github release;github codeowners;worktree prune;pr;task aggregate;task decompose'.split(';'))
assert len(preview)==18
special={
 'init':('requires-human-decision','Absent-token owner bootstrap is a legacy fallback; inherited unresolved identity is refused. TRUST blanket always-runs language is stale. New Rust bootstrap authority needs an explicit decision.'),
 'report':('requires-human-decision','TRUST describes base report usable by RO, but current Mutates dispatcher applies RW before handler. Decide distinct local diagnostic/export/publish actions; do not silently widen.'),
 'escalate':('requires-human-decision','Current Mutates dispatcher refuses RO even for local EventHelp proposal. TRUST documents contradiction. Split proposal append from public GitHub publication before port.'),
 'taint':('requires-human-decision','Current command is classified Mutates although its observed source walk is read-only. Decide query classification; no runtime reproduction performed.'),
 'catalog':('requires-human-decision','Optional wiki publication follows local catalog effects. Specify truthful partial completion/refusal instead of blanket success for failed outward effect.'),
 'ship':('requires-human-decision','Coordinator may compose VCS and release effects. Maintainer publication authority must remain separate from source integration; approve explicit action boundaries.'),
 'github release':('requires-human-decision','Exact project/tag maintainer publication action; RW alone is not a new Rust publication design.'),
 'run':('requires-human-decision','Arbitrary shell effects cannot be inferred from a declared read/write label. Decide supported execution and host sandbox policy before port.'),
 'provider readiness':('requires-human-decision','Optional auth-status may invoke a provider; split pure readiness and process/credential probe effects before exposing new runtime.'),
}
# Read exact explicit-auth classifiers rather than treating all native names as bypasses.
explicit_paths=set()
for path, name in [
 ('internal/features/nativegovernance/nativegovernance.go','AuthenticatesExplicitly'),
 ('internal/features/nativegovernance/external.go','ExternalAuthenticatesExplicitly'),
 ('internal/features/nativegovernance/integration.go','IntegrationAuthenticatesExplicitly'),
 ('internal/features/nativegovernance/modelrouting.go','ModelRoutingAuthenticatesExplicitly'),
 ('internal/features/nativegovernance/verification.go','VerificationRoutingAuthenticatesExplicitly'),
 ('internal/features/deviceauth/deviceauth.go','AuthenticatesExplicitly'),
 ('internal/features/rolemanagement/rolemanagement.go','AuthenticatesExplicitly'),
 ('internal/features/ghmirror/exact_issue_link_auth.go','AuthenticatesExplicitly')]:
    body=(root/path).read_text().split('func '+name+'(',1)[1].split('\nfunc ',1)[0]
    explicit_paths.update(re.findall(r'"([^"]+)"',body))
output=[]
for x in sorted(commands,key=lambda x:x['path']):
    path=x['path']; fields=x['fields']; mentions=[{'path':'docs/TRUST.md','startLine':i+1,'endLine':i+1} for i,l in enumerate(trust) if f'`{path}`' in l]
    assert mentions,path
    tables=[{'source':{'path':'docs/TRUST.md','startLine':i+1,'endLine':i+1},'row':l} for i,l in enumerate(trust) if l.startswith('| `'+path+'` |')]
    explicit=path in explicit_paths
    auth='independent-feature-authentication' if explicit else 'shared-legacy-mutation-authorizer'
    disposition,reason=special.get(path,('redesign','Represent this legacy capability as an explicit typed action/effect contract or retain it behind a legacy adapter; not implemented in the first Rust kernel.'))
    if path.startswith('native ') and path.endswith(' upgrade'):
        disposition='retire';reason='Retire this Go-native storage/schema upgrade name from the new canonical Rust surface; keep legacy upgrade responsibility in Go adapter until an explicitly approved migration. This does not delete existing Go support.'
    owner='native-adapter-owner' if explicit else ('vcs-adapter-owner' if x['source'].split('/')[2] in ['vcs','ship'] else 'legacy-adapter-owner')
    if disposition=='requires-human-decision':owner='maintainer-with-'+owner
    metadata={'mutates':True,'json':fields.get('JSON')=='true','runExpression':fields.get('Run'), 'effectFreeDryRun':path in preview,'valueFlagOverrides':{'github push':['milestone-plan'],'events reconcile':['project','archive-class','apply-safe'],'provider readiness':['config']}.get(path,[]),'readOnlyShape':{'project show':'No landing-mode or landing-base option','reconcile':'No apply-safe option'}.get(path)}
    output.append({'path':path,'source':{'path':x['source'],'startLine':x['line'],'endLine':x['line']},'factoryExpansion':x.get('factoryExpansion',False),'declaredMetadata':metadata,'authRoute':auth,'authRouteSource':source('internal/cli/cli.go','func refuseUngrantedMutation'),'trustSources':mentions,'trustRows':tables,'effectSummary':tables[0]['row'].split('|')[2].strip() if tables else fields.get('Brief','See command handler and cited trust prose'),'effectCoverage':'documented row; static audit only' if tables else 'brief and prose references; handler-level effect inventory still required','disposition':disposition,'reason':reason,'verificationOwner':owner,'check':f'Before enabling {path!r} or its successor, verify exact schema, authenticated actor, scope, observable effects and recovery against cited sources; add causal refusal/effect tests. Existing Go reference: TestEveryCommandDeclaresItsCapability and TestMutatingCommandsRefuseReadOnlyAgents (not run by this audit).','implementationStatus':'not-implemented-in-new-core','decisionStatus':'unresolved' if disposition=='requires-human-decision' else 'proposed','evidenceStatus':'static source inventory; not fresh runtime verification'})

inventory={'schemaVersion':1,'sourceRevision':REV,'method':'Go AST command literals plus explicit source-audited ExternalCommands/IntegrationCommands factory expansion; exact156-path equality with Go capability drift guard. Not a second runtime command registry.','mutatingCommands':output,'limitations':['Legacy Mutates is the dispatcher RW classification, not an exhaustive physical-write or side-effect census.','RO proposal/event routes and independently authenticated reads can have effects outside this156-path set.','Trust row excerpts and historical comments are source evidence only; no old incident was reproduced and no Go test was executed by this audit.','Readiness, handler effects and authority need independent future verification before enabling any legacy adapter.']}
(HERE/'command-inventory.json').write_text(json.dumps(inventory,indent=2,ensure_ascii=False)+'\n')
unresolved=[r['id'] for r in rows if r['decisionStatus']=='unresolved']+[r['path'] for r in output if r['decisionStatus']=='unresolved']
ledger={'schemaVersion':1,'sourceRevision':REV,'format':'JSON syntax, valid YAML1.2; no YAML parser dependency required','status':'proposed source-audited migration ledger; not runtime qualification','dispositionDefinitions':{'preserve':'Keep the behavioral obligation, without promising old command or wire compatibility.','redesign':'Retain the concern through explicit new typed contracts or a legacy adapter; this row is not implemented.','retire':'Do not carry this Go-specific mechanism into the new canonical surface; current Go support is not removed.','requires-human-decision':'Unresolved policy or source/documentation conflict; an accountable maintainer must decide before implementation/compatibility claims.'},'rules':rows,'commandInventory':'command-inventory.json','coverage':{'sourceRuleCount':len(rows),'coveredRuleCount':len(rows),'mutatingCommandCount':len(output),'coveredMutatingCommandCount':len(output),'literalCommandCount':148,'factoryExpandedCommandCount':8,'documentedTableRowCount':sum(bool(x['trustRows']) for x in output),'previewExceptionCount':len(preview),'unresolvedCount':len(unresolved),'unresolved':unresolved,'runtimeVerifiedRuleCount':0,'allMistakesCovered':False,'readiness':'blocked: unresolved decisions and planned/unimplemented rows; enumeration does not establish implementation or historical incident coverage'},'verificationOwnersAre':'proposed accountable roles, not grants, named authenticated reviewers, completed checks or approvals','initialSliceBoundary':{'allowed':['One pure InspectProtocol query','Typed refusal for unavailable mutation','Shared ActionSpec decoding/policy/result and generated catalog'], 'excluded':['Go credential reuse','Native authority migration','Filesystem/process/storage effects','Task writer','Plugin execution','Old command compatibility claim','Farm rendering']}}
(HERE/'invariants.yaml').write_text(json.dumps(ledger,indent=2,ensure_ascii=False)+'\n')
print(json.dumps({'rules':len(rows),'commands':len(output),'tableRows':ledger['coverage']['documentedTableRowCount'],'unresolved':len(unresolved),'allMistakesCovered':False}))
