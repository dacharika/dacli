// The data contract — a verbatim mirror of the Go `dashboardState` structs in
// `internal/features/dashboard/dashboard.go`. These are the ONLY fields the UI
// may bind to (DESIGN.md §0). Adding a field here without a matching server
// change is a spec violation: the UI never derives data the snapshot lacks.

/** Canonical task statuses, in the order they appear everywhere in the UI. */
export const STATUSES = ['open', 'active', 'blocked', 'done'] as const
export type Status = (typeof STATUSES)[number]

/** `counts` is a PARTIAL map — a status key is absent when its count is zero. */
export type StatusCounts = Partial<Record<Status, number>>

export interface BurndownDay {
  /** `YYYY-MM-DD`, already sorted chronologically by the server. */
  day: string
  points: number
  task_ids?: string[]
}

export interface Burndown {
  done_points: number
  remaining_points: number
  /** Tasks with no PERT estimate; contribute to totals/counts but not points. */
  unestimated: number
  /** Done points that landed each day, chronological. May be empty. */
  per_day: BurndownDay[]
  hidden_days?: number
}

/** One task in the dependency DAG. `critical`/`slack`/`early_start` are
 * meaningful only when the graph is `scheduled` and the node is in the
 * scheduled (open, non-blocked) subset; otherwise `slack` is -1 and `critical`
 * is false. */
export interface GraphNode {
  id: string
  seq: number
  slug: string
  title: string
  status: Status
  /** PERT expected duration; 0 when unestimated. */
  points: number
  estimated: boolean
  /** On the zero-slack critical path. */
  critical: boolean
  /** -1 when unscheduled (done, blocked, or the graph is not scheduled). */
  slack: number
  early_start: number
}

/** A dependency edge: `from` must satisfy its type before `to`. Both ends are
 * always ids present in `Graph.nodes`. */
export interface GraphEdge {
  from: string
  to: string
  /** FS | SS | FF | SF (FS when unspecified). */
  type: string
}

/**
 * The task dependency DAG plus, when the open tasks are schedulable, the CPM
 * critical path drawn over them (what `internal/spm/criticalpath.go` computes).
 * The DAG (`nodes` + `edges`) is always present; the critical-path overlay is
 * best-effort: `scheduled` is true only when every open task is estimated and
 * the open subgraph is acyclic, else `note` explains the absence and the DAG
 * still renders. 0-safe: an empty project yields empty arrays and scheduled
 * false.
 */
export interface Graph {
  /** The project slug this graph covers; '' when it spans every project. */
  project: string
  nodes: GraphNode[]
  edges: GraphEdge[]
  /** Zero-slack chain in topological order (task ids); empty when unscheduled. */
  critical_path: string[]
  /** Project duration in Te units; 0 when unscheduled. */
  duration: number
  scheduled: boolean
  /** Why the critical path is absent (unestimated, cycle, or no open tasks). */
  note: string
  projection: GraphProjection
}

export interface GraphProjection {
  mode: 'operational' | 'focus' | 'history' | ''
  rule: string
  focus?: string
  statuses: string[]
  page: number
  limit: number
  total_nodes: number
  visible_nodes: number
  hidden_nodes: number
  total_edges: number
  visible_edges: number
  hidden_edges: number
  critical_total: number
  has_more: boolean
}

export interface Project {
  slug: string
  title: string
  stage: string
  /** Task count across all statuses. */
  total: number
  counts: StatusCounts
  burndown: Burndown
  /** Dependency DAG + CPM critical path for this project. */
  graph: Graph
}

/** The honest per-agent activity the server derives from the task status and
 * transcript — never a guess from RAM/CPU. `thinking` = last line is assistant
 * prose; `acting` = last line is a `[tool: X]` marker; `waiting` = nothing
 * rendered yet (fresh spawn or a text runtime buffering to exit); `stalled` =
 * the transcript has frozen past the server's stall window while the process
 * is still alive; `blocked` = the agent's task has an outstanding `dacli ask`;
 * `silent` = a text runtime has stayed quiet past the stall window (buffered
 * output is normal briefly, not for minutes). */
export const AGENT_STATES = [
  'thinking',
  'acting',
  'waiting',
  'stalled',
  'blocked',
  'silent',
] as const
export type AgentState = (typeof AGENT_STATES)[number]

export interface Agent {
  run_id: string
  child: string
  task: string
  role: string
  runtime: string
  pid: number
  /** RFC3339 UTC. */
  started: string
  /** Honest activity derived server-side from the transcript. */
  state: AgentState
  /** Uptime, seconds. */
  runtime_secs: number
  /** transcript.log mtime, RFC3339 UTC; falls back to `started`. */
  last_activity: string
  /** Same-origin read-only link to the run's rendered transcript. */
  transcript_url: string
  /** Same-origin read-only link to the run's uncommitted diff (`git diff HEAD`). */
  diff_url: string
}

/** One day of burn: output tokens and USD summed across every usage-bearing run
 * that started that day, the run count, and the derived per-run intensity. */
export interface BurnPoint {
  /** `YYYY-MM-DD` UTC, already sorted chronologically by the server. */
  day: string
  tokens: number
  cost_usd: number
  runs: number
  run_ids?: string[]
  /** tokens / runs; 0 when the day had no runs. */
  per_run: number
}

/** One calibrated agent band's per-run token expectation (role×model×runtime). */
export interface BurnBand {
  /** `role/model/runtime`. */
  band: string
  role: string
  /** Median output tokens per run for the band. */
  expected: number
  n: number
  /** n >= 10 — the store's authoritative-calibration gate. */
  calibrated: boolean
}

/** One project's live governor window spend, read from the persisted snapshot. */
export interface BurnWindow {
  project: string
  spent: number
  /** RFC3339 UTC, or '' when no window has opened yet. */
  start: string
}

/**
 * The token/cost burn story: actuals over time (`series`) measured against the
 * calibrated `ceiling`. `alert` is the server's verdict that `rate` is at or
 * above `alert_at`× the ceiling — the signal the chart must YELL on, not draw
 * as a passive line. 0-safe: an empty workspace yields an empty series and a
 * zero ceiling, and `alert` is false (nothing to compare against).
 */
export interface Burn {
  native?: NativeSnapshot[]
  /** What series/ceiling/rate are measured in — `output_tokens`. */
  unit: string
  series: BurnPoint[]
  hidden_points?: number
  bands: BurnBand[]
  windows: BurnWindow[]
  /** Calibrated per-run token norm; 0 when there is no token history. */
  ceiling: number
  /** Current burn intensity: the latest day's per-run tokens. */
  rate: number
  /** rate / ceiling; 0 when ceiling is 0. */
  ratio: number
  /** ratio >= alert_at (and ceiling > 0): the chart yells. */
  alert: boolean
  /** The multiple that yells (1.5), echoed so the client thresholds identically. */
  alert_at: number
}

export type LoopOperationState =
  | 'not-started'
  | 'running'
  | 'idle'
  | 'sleeping-budget'
  | 'waiting-ci'
  | 'waiting-review'
  | 'waiting-owner'
  | 'halted-policy'
  | 'externally-unknown'
  | 'completed'
  | 'corrupt'

export interface LoopTokenAmount {
  unit: string
  limit: number
  spent: number
  reserved: number
  remaining?: number | null
}

export interface LoopRunReservation {
  task: string
  run_id?: string
  tokens: number
  state: string
  outcome?: string
  usage?: number | null
  observed_at?: string
}

export interface LoopOperationTask {
  task: string
  run_id?: string
  phase: string
  generation: number
  updated_at?: string
  role?: string
  runtime?: string
  model?: string
  grant?: string
  claim_count: number
  capacity_fit?: boolean | null
  task_points?: number
  role_limit?: number
  override?: string
  verification_cwd?: string
  verification_argv?: string
}

export interface LoopRouteCandidate {
  role: string
  runtime: string
  model: string
  eligible: boolean
  exclusions?: string[]
  score: {
    cost_tier: number
    tokens_per_completed: number
    token_samples: number
    first_pass_success: number
    success_samples: number
    latency_seconds: number
    domain_fit: number
    total: number
  }
}

export interface LoopOperationRouting {
  task: string
  selected: { role?: string; runtime?: string; model?: string }
  candidates: LoopRouteCandidate[]
  source: string
  uplift?: string
  freshness: string
}

export interface LoopPreflightPhase {
  phase: string
  task?: string
  role?: string
  runtime?: string
  model?: string
  grant?: string
  verdict: string
  classification: string
  evidence?: string
  remediation?: string
  token_control?: string
  output_contract?: string
}

export interface LoopOperationResponse {
  schema: 'loop-operation/v1'
  generated: string
  project: string
  state: {
    value: LoopOperationState
    freshness: 'fresh' | 'stale' | 'partial' | 'missing' | 'corrupt'
    source: string
    observed_at?: string
    cycle: number
    generation: number
    phase?: string
    checkpoint?: string
    retryable?: boolean | null
    halt_class?: string
    reason?: string
    next_action: string
    last_checkpoint?: string
  }
  wave: { requested_width: number; allocated_width: number; live_width: number }
  budget: {
    mode: 'enforceable' | 'advisory' | 'unknown' | ''
    observed_at?: string
    window_reset_at?: string
    cycle: LoopTokenAmount
    rolling: LoopTokenAmount
    runs: LoopRunReservation[]
    review_reservation: number
    recovery_reserve: number
    unallocated?: number | null
    unknown_usage_runs: string[]
    accounting_boundary: string
  }
  tasks: LoopOperationTask[]
  active_runs: Array<{
    run_id: string
    agent_id: string
    task: string
    role?: string
    runtime?: string
    state: string
  }>
  routing: LoopOperationRouting[]
  preflight: LoopPreflightPhase[]
  harness: { mode: string; allowed: string[]; source: string }
  warnings: string[]
}

/**
 * One role on the team roster (dacli 226). A role is the only thing that
 * mechanically changes what an agent can do — which skills load, which paths are
 * in scope, which runtime and model it costs, how many may run at once — so this
 * is the whole configuration an operator would otherwise have to read out of
 * `.dacli/roles/*.md` by hand. Every list field is always an array, never null.
 */
export interface Role {
  name: string
  summary: string
  /** Lifecycle function phase gating acts on; '' means the role opts out. */
  kind: string
  /** Default capability a spawn into this role receives (ro | rw). */
  grant: string
  runtime: string
  model: string
  /** Concurrent-agent cap; 0 means uncapped. */
  wip: number
  /** Largest task expected size (Te) this role may take; 0 means uncapped. */
  max_points: number
  scope: string[]
  out_of_scope: string[]
  skills: string[]
  shortcuts: string[]
  escalate_to: string[]
  /** Non-retired agents currently holding the role — the WIP numerator. */
  active_agents: number
  /** The server's verdict that another spawn would break the cap. */
  wip_exceeded: boolean
  /** Whether the role file carries standing instructions below its frontmatter —
   * the difference between a role that is described and one that is defined. */
  has_prompt: boolean
}

export interface DashboardState {
  /** RFC3339 UTC; when this snapshot was built. */
  generated: string
  /** Unsynced child events, as `dacli status` reports. */
  pending_events: number
  projects: Project[]
  /** Newest-first and already liveness-filtered by the server. */
  agents: Agent[]
  /** Token/cost burn-rate over time vs the calibrated ceiling. */
  burn: Burn
  /** The team roster, sorted by name. Identical to what `/api/roles` serves. */
  roles: Role[]
}

/** Typed endpoint envelopes. The Vue application consumes these independently;
 * DashboardState remains the compatibility contract for the legacy page. */
export interface OverviewResponse {
  generated: string
  project_count: number
  task_count: number
  counts: StatusCounts
  pending_events: number
  live_agents: number
}

export interface ProjectsResponse {
  generated: string
  projects: Project[]
}

export interface AgentsResponse {
  generated: string
  agents: Agent[]
}

export interface TaskSummary {
  id: string
  project: string
  seq: number
  slug: string
  title: string
  status: Status
  priority: string
  owner: string
  points: number
  estimated: boolean
}

export type AgentTaskSummary = TaskSummary

export interface TasksResponse {
  generated: string
  tasks: TaskSummary[]
}

export interface TaskEstimate {
  optimistic: number
  probable: number
  pessimistic: number
  expected: number
}

export interface AcceptanceBox {
  text: string
  done: boolean
}

export interface TaskDependency {
  ref: string
  type: string
  id: string
  title: string
  status: Status | ''
  resolved: boolean
}

export interface TaskLogEntry {
  at: string
  text: string
}

export interface TaskDetail extends TaskSummary {
  native?: NativeSnapshot
  estimate: TaskEstimate | null
  so_that: string
  context: string
  acceptance: AcceptanceBox[]
  acceptance_done: number
  acceptance_total: number
  deps: TaskDependency[]
  parent: string
  log: TaskLogEntry[]
}

export interface TaskDetailResponse {
  generated: string
  task: TaskDetail
}

export interface TaskEvent {
  id: string
  kind: string
  actor: string
  about: string
  origin: string
  against: string
  applied: boolean
  at: string
  body: string
}

export interface TaskEventsResponse {
  generated: string
  task: string
  limit: number
  truncated: boolean
  events: TaskEvent[]
}

export type EventStateFilter = 'all' | 'pending' | 'applied'

export interface ActivityEvent {
  id: string
  kind: string
  label: string
  category:
    | 'refusal'
    | 'finding'
    | 'ask'
    | 'review'
    | 'reconciliation'
    | 'handoff'
    | 'delivery'
    | 'ownership'
    | 'proposal'
    | 'activity'
  actor: string
  about: string
  origin: string
  against: string
  applied: boolean
  at: string
  body: string
  related_task?: string
  related_agent?: string
}

export interface ActivityFilters {
  task?: string
  project?: string
  kind?: string
  actor?: string
  state: EventStateFilter
  range: '24h' | '7d' | '30d' | 'all'
}

export interface ActivityResponse {
  generated: string
  task: string
  limit: number
  cursor?: string
  next_cursor?: string
  truncated: boolean
  partial: boolean
  unreadable_records: number
  filters: ActivityFilters
  events: ActivityEvent[]
}

export type AttentionSeverity = 'critical' | 'high' | 'medium' | 'low'

export interface AttentionEvidence {
  kind: string
  id: string
  url: string
  observed_at: string
  confidence: 'high' | 'medium' | 'low'
}

export interface OperatorAttentionItem {
  id: string
  code: string
  severity: AttentionSeverity
  affected: { project: string; task?: string; run?: string; pr?: string }
  first_observed: string
  last_observed: string
  freshness: 'fresh' | 'stale' | 'missing'
  retryable: boolean
  summary: string
  next_action: string
  link: string
  evidence: AttentionEvidence[]
  occurrences: number
  duration_seconds: number
  critical_path: boolean
  confidence: 'high' | 'medium' | 'low'
  rank: number
  rank_reason: string
}

export interface OperatorAttentionResponse {
  native?: NativeSnapshot[]
  schema: 'operator-attention/v1'
  generated: string
  project?: string
  alerts: OperatorAttentionItem[]
  ranking_rule: string
}

export interface AgentRun {
  run_id: string
  task: string
  role: string
  runtime: string
  pid: number
  started: string
  live: boolean
  transcript_url: string
  diff_url: string
}

export interface AgentDetail {
  id: string
  role: string
  parent: string
  grant: string
  retired: boolean
  children: string[]
  tasks: AgentTaskSummary[]
  runs: AgentRun[]
}

export interface AgentDetailResponse {
  generated: string
  agent: AgentDetail
}

export interface RolesResponse {
  generated: string
  roles: Role[]
}

export type DeliverySpanStatus =
  'complete' | 'current' | 'pending' | 'skipped' | 'refused' | 'unknown'

export interface DeliverySpan {
  phase: string
  status: DeliverySpanStatus
  started?: string
  ended?: string
  /** null means unknown; the server never fabricates a zero duration. */
  duration_ms: number | null
  source: string
  freshness: string
  detail: string
  next_action: string
  contract?: string
  verdict?: string
  correction?: number
}

export interface DeliveryAttempt {
  attempt: number
  run_id: string
  agent_id: string
  role: string
  runtime: string
  model: string
  generation: number
  started: string
  outcome: string
  recovered: boolean
  usage: {
    available: boolean
    input_tokens: number
    output_tokens: number
    turns: number
    cost_usd: number
  }
  identity: {
    task_id: string
    run_id: string
    branch: string
    commit_sha: string
    tree_sha: string
    pr_url: string
    pr_generation: number
  }
  diagnosis: {
    class:
      | 'pending'
      | 'policy-refusal'
      | 'external-api-unknown'
      | 'failed'
      | 'merged-not-accepted'
      | 'accepted-on-current-tree'
    detail: string
    next_action: string
  }
  pull_requests?: Array<{
    url: string
    generation: number
    state:
      | 'current'
      | 'superseded'
      | 'merged'
      | 'closed-unmerged'
      | 'superseded-merged'
      | 'superseded-closed'
    merge_sha?: string
    observed_at?: string
  }>
  spans: DeliverySpan[]
}

export interface DeliveryTimelineResponse {
  schema: 'delivery-attempt-timeline/v1'
  generated: string
  task: {
    id: string
    sequence: number
    generation: number
    project: string
    title: string
    status: Status
  }
  attempts: DeliveryAttempt[]
  summary: string
  refusal?: string
}

export interface DeliveryAttentionResponse {
  schema: 'delivery-attention/v1'
  generated: string
  item?: {
    task_id: string
    project: string
    title: string
    branch: string
    commit_sha?: string
    tree_sha?: string
    class: DeliveryAttempt['diagnosis']['class']
    detail: string
    next_action: string
  }
}

export interface GraphResponse extends Graph {
  generated: string
}

export interface BurnResponse extends Burn {
  generated: string
}

export type OutcomeState = 'complete' | 'partial' | 'unknown' | 'stale' | 'advisory'

export interface ChartPointContract {
  id: string
  label: string
  value: number | null
  display: string
  href?: string
  evidence_count: number
}

export interface ChartContract {
  id: string
  title: string
  metric_definition: string
  unit: string
  window: string
  source: string
  freshness: string
  coverage: string
  comparison?: string
  state: 'loading' | 'empty' | 'live' | 'stale' | 'partial' | 'error'
  state_detail: string
  summary: string
  points: ChartPointContract[]
  hidden_resolution?: string
}

export interface OutcomeEvidence {
  tasks: string[]
  runs: string[]
  truncated: boolean
}

export interface OutcomeMeasure {
  key: string
  label: string
  value: number | null
  unit: string
  sample_size: number
  eligible: number
  coverage: number
  state: OutcomeState
  provenance: string
  caveat?: string
  evidence: OutcomeEvidence
}

export interface OutcomeMetric {
  key: string
  label: string
  current: OutcomeMeasure
  previous: OutcomeMeasure
  change: number | null
  trend: 'up' | 'down' | 'flat' | 'not-comparable'
}

export interface OutcomeBreakdown {
  dimension: string
  key: string
  size_band?: string
  current: OutcomeMeasure
  previous: OutcomeMeasure
  comparable: boolean
  caveat?: string
  evidence: OutcomeEvidence
}

export interface OutcomeAnalyticsResponse {
  schema: 'outcome-analytics/v1'
  generated: string
  project: string
  current_window: { start: string; end: string; days: number }
  previous_window: { start: string; end: string; days: number }
  metrics: OutcomeMetric[]
  breakdowns: OutcomeBreakdown[]
  series: Array<{
    day: string
    completed: number
    runs: number
    tokens: number
    evidence: OutcomeEvidence
  }>
  performance: {
    tasks_scanned: number
    runs_scanned: number
    series_points: number
    build_ms: number
    evidence_cap: number
    cache: string
    cache_entries: number
  }
  notes: string[]
}

/** A zero-safe empty burn — the getter's fallback before the first snapshot and
 * a resilient default if a payload ever omits the field. */
export function emptyBurn(): Burn {
  return {
    unit: 'output_tokens',
    series: [],
    hidden_points: 0,
    bands: [],
    windows: [],
    ceiling: 0,
    rate: 0,
    ratio: 0,
    alert: false,
    alert_at: 1.5,
  }
}

/** A zero-safe empty graph — the fallback when a project has no graph yet, so
 * the DAG view never binds to undefined. */
export function emptyGraph(): Graph {
  return {
    project: '',
    nodes: [],
    edges: [],
    critical_path: [],
    duration: 0,
    scheduled: false,
    note: '',
    projection: {
      mode: '',
      rule: '',
      statuses: [],
      page: 1,
      limit: 0,
      total_nodes: 0,
      visible_nodes: 0,
      hidden_nodes: 0,
      total_edges: 0,
      visible_edges: 0,
      hidden_edges: 0,
      critical_total: 0,
      has_more: false,
    },
  }
}

/**
 * The poller's tri-state, the sole driver of every global state view (DESIGN.md
 * §6). `loading` before the first success; `live` once a snapshot is in;
 * `error` on a failed poll (the last good snapshot is retained, not blanked).
 */
export type Phase = 'loading' | 'live' | 'partial' | 'error'

export interface NativeRecord {
  kind: string
  id: string
  task_id: string
  attempt_id?: string
  lease_id?: string
  revision_id?: string
  reviewer_id?: string
  actor_id?: string
  decision_id?: string
  evidence_ids?: string[]
  commit?: string
  tree?: string
  check_id?: string
  provenance?: string
  state: string
  freshness: string
  observed_at: string
  expires_at?: string
  claims?: string[]
  eligibility?: string
}
export interface NativeSnapshot {
  schema: 'native-dashboard/v1'
  state: string
  project: string
  observed_at: string
  authority_version: number
  limited: boolean
  services: Record<string, string>
  records: NativeRecord[]
  attention: { id: string; task_id: string; category: string; summary: string; freshness: string }[]
  spend: {
    state: string
    provenance: string
    enforcement: string
    unit: string
    estimated_spent: number | null
    estimated_reserved: number | null
    estimated_limit: number | null
    provider_tokens: number | null
    provider_cost_usd: number | null
    unknown_retained: boolean
  }
}
