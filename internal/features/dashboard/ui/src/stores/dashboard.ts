import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import type {
  Agent,
  AgentDetail,
  AgentDetailResponse,
  AgentsResponse,
  ActivityFilters,
  ActivityResponse,
  Burn,
  BurnResponse,
  DeliveryTimelineResponse,
  Graph,
  GraphResponse,
  LoopOperationResponse,
  OperatorAttentionResponse,
  OutcomeAnalyticsResponse,
  OverviewResponse,
  Phase,
  Project,
  ProjectsResponse,
  Role,
  RolesResponse,
  TaskDetail,
  TaskDetailResponse,
  TaskEventsResponse,
  TaskSummary,
  TasksResponse,
} from '@/types'
import { emptyBurn, emptyGraph } from '@/types'

// Fast-changing process state stays near the old two-second heartbeat. Durable
// project/burn/graph projections refresh less often, and the roster is almost
// static. These intervals are exported so request-count tests prove the policy
// instead of sleeping and hoping (issue #932).
export const FAST_POLL_MS = 2_000
export const SLOW_POLL_MS = 10_000
export const ROSTER_POLL_MS = 60_000

type SurfaceName =
  | 'overview'
  | 'projects'
  | 'tasks'
  | 'agents'
  | 'burn'
  | 'operations'
  | 'outcomes'
  | 'roles'
  | 'graph'
  | 'timeline'
  | 'attention'
  | 'events'
const SURFACE_NAMES: readonly SurfaceName[] = [
  'overview',
  'projects',
  'tasks',
  'agents',
  'burn',
  'operations',
  'outcomes',
  'roles',
  'graph',
  'timeline',
  'attention',
  'events',
]

export type DashboardRouteName =
  'overview' | 'work' | 'agents' | 'team' | 'activity' | 'delivery' | 'unknown'

// The route is the authority for what the browser observes (issue #941). The
// overview heartbeat stays global for connection/pending-event context; every
// heavier projection is fetched only for the route that can render it.
const ROUTE_SURFACES: Record<DashboardRouteName, readonly SurfaceName[]> = {
  overview: ['overview', 'projects', 'attention'],
  work: ['overview', 'projects', 'tasks'],
  agents: ['overview', 'projects', 'operations', 'outcomes', 'agents', 'burn'],
  team: ['overview', 'roles', 'agents'],
  activity: ['overview', 'projects', 'events'],
  delivery: ['overview', 'projects', 'graph', 'timeline', 'attention'],
  unknown: ['overview'],
}

interface Surface<T> {
  data: T
  phase: Exclude<Phase, 'partial'>
  error: string | null
  status: number | null
  generated: string | null
  lastOk: number | null
  generation: number
  controller: AbortController | null
}

type SurfaceStatus = Pick<Surface<unknown>, 'phase' | 'error' | 'generated' | 'lastOk'>

function surface<T>(data: T): Surface<T> {
  return {
    data,
    phase: 'loading',
    error: null,
    status: null,
    generated: null,
    lastOk: null,
    generation: 0,
    controller: null,
  }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * Per-surface dashboard state (issue #932).
 *
 * The previous store polled `/api/state` every two seconds, rebuilding and
 * transferring every graph, role and burn record even when the user was not
 * looking at them. Each canonical GET projection now owns its freshness and
 * failure state. A request generation and AbortController prevent a slow,
 * older response from overwriting a newer observation.
 */
export const useDashboardStore = defineStore('dashboard', () => {
  const overviewSurface = ref(surface<OverviewResponse | null>(null))
  const projectsSurface = ref(surface<Project[]>([]))
  const tasksSurface = ref(surface<TaskSummary[]>([]))
  const agentsSurface = ref(surface<Agent[]>([]))
  const agentDetailSurface = ref(surface<AgentDetail | null>(null))
  const burnSurface = ref(surface<Burn>(emptyBurn()))
  const operationSurface = ref(surface<LoopOperationResponse | null>(null))
  const outcomeSurface = ref(surface<OutcomeAnalyticsResponse | null>(null))
  const outcomeRange = ref<7 | 30 | 90>(30)
  const rolesSurface = ref(surface<Role[]>([]))
  const graphSurface = ref(surface<Graph>(emptyGraph()))
  const graphMode = ref<'operational' | 'history'>('operational')
  const graphStatuses = ref<string[]>([])
  const graphFocus = ref('')
  const graphPage = ref(1)
  const timelineSurface = ref(surface<DeliveryTimelineResponse | null>(null))
  const attentionSurface = ref(surface<OperatorAttentionResponse | null>(null))
  const taskDetailSurface = ref(surface<TaskDetail | null>(null))
  const taskEventsSurface = ref(surface<TaskEventsResponse | null>(null))
  const activitySurface = ref(surface<ActivityResponse | null>(null))
  const activityQuery = ref<ActivityFilters & { cursor?: string; limit: number }>({
    state: 'all',
    range: '7d',
    limit: 50,
  })
  const selectedSlug = ref('')
  const selectedTaskRef = ref('')
  const selectedAgentID = ref('')
  const paused = ref(false)

  let running = false
  const activeRoute = ref<DashboardRouteName>('overview')
  const routeGeneration = ref(0)
  let activeSurfaces = new Set<SurfaceName>(ROUTE_SURFACES.overview)
  const timers = new Map<SurfaceName, ReturnType<typeof setTimeout>>()
  let activeFetch: typeof fetch = fetch
  const agentDetailCache = new Map<
    string,
    { data: AgentDetail; generated: string | null; observedAt: number | null }
  >()
  const taskDetailCache = new Map<string, { data: TaskDetail; generated: string | null }>()
  const taskEventsCache = new Map<string, { data: TaskEventsResponse; generated: string | null }>()

  async function request<TPayload, TValue>(
    target: { value: Surface<TValue> },
    url: string,
    select: (payload: TPayload) => TValue,
    fetchImpl: typeof fetch = activeFetch,
  ): Promise<boolean> {
    const generation = target.value.generation + 1
    target.value.generation = generation
    target.value.controller?.abort()
    const controller = new AbortController()
    target.value.controller = controller
    try {
      const res = await fetchImpl(url, { cache: 'no-store', signal: controller.signal })
      if (!res.ok) {
        target.value.status = res.status
        let detail = ''
        if (res.status === 400) {
          detail = (await res.text())
            .replace(/[\u0000-\u001f\u007f]+/g, ' ')
            .trim()
            .slice(0, 256)
        }
        throw new Error(`HTTP ${res.status}${detail ? `: ${detail}` : ''}`)
      }
      const payload = (await res.json()) as TPayload & { generated?: string }
      if (target.value.generation !== generation) return false
      target.value.data = select(payload)
      target.value.generated = payload.generated ?? null
      target.value.lastOk = Date.now()
      target.value.error = null
      target.value.status = null
      target.value.phase = 'live'
      return true
    } catch (err) {
      if (target.value.generation !== generation || controller.signal.aborted) return false
      target.value.error = message(err)
      target.value.phase = 'error'
      return false
    } finally {
      if (target.value.generation === generation) target.value.controller = null
    }
  }

  const pollOverview = (fetchImpl: typeof fetch = activeFetch) =>
    request<OverviewResponse, OverviewResponse | null>(
      overviewSurface,
      '/api/overview',
      (payload) => payload,
      fetchImpl,
    )

  async function pollProjects(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const ok = await request<ProjectsResponse, Project[]>(
      projectsSurface,
      '/api/projects',
      (payload) => payload.projects ?? [],
      fetchImpl,
    )
    if (!ok) return false
    const projects = projectsSurface.value.data
    if (!projects.some((project) => project.slug === selectedSlug.value)) {
      selectedSlug.value = projects[0]?.slug ?? ''
      if (selectedSlug.value && activeRoute.value === 'delivery') void pollGraph(fetchImpl)
      else if (selectedSlug.value && activeRoute.value === 'work') void pollTasks(fetchImpl)
      else if (selectedSlug.value && activeRoute.value === 'agents') void pollOperation(fetchImpl)
      else resetGraph()
    }
    return true
  }

  async function pollTasks(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const slug = selectedSlug.value
    if (!slug) {
      resetTasks()
      return true
    }
    const ok = await request<TasksResponse, TaskSummary[]>(
      tasksSurface,
      `/api/tasks?project=${encodeURIComponent(slug)}`,
      (payload) => payload.tasks ?? [],
      fetchImpl,
    )
    if (ok && selectedTaskRef.value && activeRoute.value === 'work') {
      await pollTaskDetail(fetchImpl)
    }
    return ok
  }

  async function pollAgents(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const ok = await request<AgentsResponse, Agent[]>(
      agentsSurface,
      '/api/agents',
      (payload) => payload.agents ?? [],
      fetchImpl,
    )
    if (ok && selectedAgentID.value && activeRoute.value === 'agents') {
      await pollAgentDetail(fetchImpl)
    }
    return ok
  }

  async function pollAgentDetail(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const id = selectedAgentID.value
    if (!id) {
      resetAgentDetail()
      return true
    }
    const ok = await request<AgentDetailResponse, AgentDetail | null>(
      agentDetailSurface,
      `/api/agent?id=${encodeURIComponent(id)}`,
      (payload) => {
        if (!payload.agent || payload.agent.id !== id) {
          throw new Error(`agent identity mismatch: requested ${id}`)
        }
        return payload.agent
      },
      fetchImpl,
    )
    if (ok && agentDetailSurface.value.data) {
      agentDetailCache.set(id, {
        data: agentDetailSurface.value.data,
        generated: agentDetailSurface.value.generated,
        observedAt: agentsSurface.value.lastOk,
      })
    }
    return ok
  }

  function resetAgentDetail(): void {
    agentDetailSurface.value.controller?.abort()
    agentDetailSurface.value.generation++
    agentDetailSurface.value.controller = null
    agentDetailSurface.value.data = null
    agentDetailSurface.value.phase = 'loading'
    agentDetailSurface.value.error = null
    agentDetailSurface.value.status = null
    agentDetailSurface.value.generated = null
    agentDetailSurface.value.lastOk = null
  }

  function selectAgent(id: string, fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    if (id === selectedAgentID.value && agentDetailSurface.value.lastOk !== null) {
      return Promise.resolve(true)
    }
    selectedAgentID.value = id
    resetAgentDetail()
    if (!id || activeRoute.value !== 'agents') return Promise.resolve(true)
    const cached = agentDetailCache.get(id)
    if (cached && cached.observedAt === agentsSurface.value.lastOk) {
      agentDetailSurface.value.data = cached.data
      agentDetailSurface.value.generated = cached.generated
      agentDetailSurface.value.lastOk = Date.now()
      agentDetailSurface.value.phase = 'live'
      return Promise.resolve(true)
    }
    return pollAgentDetail(fetchImpl)
  }

  const pollBurn = (fetchImpl: typeof fetch = activeFetch) =>
    request<BurnResponse, Burn>(
      burnSurface,
      '/api/burn',
      (payload) => ({
        unit: payload.unit,
        native: payload.native,
        series: payload.series,
        hidden_points: payload.hidden_points,
        bands: payload.bands,
        windows: payload.windows,
        ceiling: payload.ceiling,
        rate: payload.rate,
        ratio: payload.ratio,
        alert: payload.alert,
        alert_at: payload.alert_at,
      }),
      fetchImpl,
    )

  async function pollOperation(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const slug = selectedSlug.value
    if (!slug) {
      resetOperation()
      return true
    }
    return request<LoopOperationResponse, LoopOperationResponse | null>(
      operationSurface,
      `/api/loop-operation?project=${encodeURIComponent(slug)}`,
      (payload) => payload,
      fetchImpl,
    )
  }

  async function pollOutcomes(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const slug = selectedSlug.value
    if (!slug) {
      resetOutcomes()
      return true
    }
    return request<OutcomeAnalyticsResponse, OutcomeAnalyticsResponse | null>(
      outcomeSurface,
      `/api/outcomes?project=${encodeURIComponent(slug)}&range=${outcomeRange.value}d`,
      (payload) => payload,
      fetchImpl,
    )
  }

  function resetOutcomes(): void {
    outcomeSurface.value.controller?.abort()
    outcomeSurface.value.generation++
    outcomeSurface.value.controller = null
    outcomeSurface.value.data = null
    outcomeSurface.value.phase = 'loading'
    outcomeSurface.value.error = null
    outcomeSurface.value.status = null
    outcomeSurface.value.generated = null
    outcomeSurface.value.lastOk = null
  }

  function setOutcomeRange(days: 7 | 30 | 90, fetchImpl: typeof fetch = activeFetch) {
    if (outcomeRange.value === days) return Promise.resolve(true)
    outcomeRange.value = days
    resetOutcomes()
    return activeRoute.value === 'agents' ? pollOutcomes(fetchImpl) : Promise.resolve(true)
  }

  function resetOperation(): void {
    operationSurface.value.controller?.abort()
    operationSurface.value.generation++
    operationSurface.value.controller = null
    operationSurface.value.data = null
    operationSurface.value.phase = 'loading'
    operationSurface.value.error = null
    operationSurface.value.status = null
    operationSurface.value.generated = null
    operationSurface.value.lastOk = null
  }

  const pollRoles = (fetchImpl: typeof fetch = activeFetch) =>
    request<RolesResponse, Role[]>(
      rolesSurface,
      '/api/roles',
      (payload) => payload.roles ?? [],
      fetchImpl,
    )

  const pollActivity = (fetchImpl: typeof fetch = activeFetch) => {
    const query = new URLSearchParams({
      state: activityQuery.value.state,
      range: activityQuery.value.range,
      limit: String(activityQuery.value.limit),
    })
    for (const key of ['project', 'task', 'kind', 'actor', 'cursor'] as const) {
      const value = activityQuery.value[key]
      if (value) query.set(key, value)
    }
    return request<ActivityResponse, ActivityResponse | null>(
      activitySurface,
      `/api/events?${query.toString()}`,
      (payload) => payload,
      fetchImpl,
    )
  }

  function configureActivity(
    next: Partial<ActivityFilters & { cursor?: string; limit: number }>,
    fetchImpl: typeof fetch = activeFetch,
    refresh = true,
  ): Promise<boolean> {
    activityQuery.value = {
      state: next.state ?? 'all',
      range: next.range ?? '7d',
      limit: next.limit ?? 50,
      project: next.project,
      task: next.task,
      kind: next.kind,
      actor: next.actor,
      cursor: next.cursor,
    }
    if (refresh && activeRoute.value === 'activity') return pollActivity(fetchImpl)
    return Promise.resolve(true)
  }

  async function pollGraph(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const slug = selectedSlug.value
    if (!slug) {
      resetGraph()
      return true
    }
    const query = new URLSearchParams({ project: slug })
    if (graphMode.value !== 'operational') query.set('mode', graphMode.value)
    if (graphFocus.value) query.set('focus', graphFocus.value)
    if (graphStatuses.value.length > 0) query.set('status', graphStatuses.value.join(','))
    if (graphMode.value === 'history') query.set('page', String(graphPage.value))
    const ok = await request<GraphResponse, Graph>(
      graphSurface,
      `/api/graph?${query.toString()}`,
      (payload) => ({
        project: payload.project,
        nodes: payload.nodes,
        edges: payload.edges,
        critical_path: payload.critical_path,
        duration: payload.duration,
        scheduled: payload.scheduled,
        note: payload.note,
        projection: payload.projection ?? emptyGraph().projection,
      }),
      fetchImpl,
    )
    // Selection may have changed while the response was in flight. The request
    // generation normally catches this; checking the payload identity makes the
    // invariant explicit even under a custom fetch implementation.
    if (ok && graphSurface.value.data.project !== selectedSlug.value) {
      graphSurface.value.generation++
      graphSurface.value.phase = 'loading'
      return false
    }
    return ok
  }

  async function setGraphQuery(
    next: Partial<{
      mode: 'operational' | 'history'
      statuses: string[]
      focus: string
      page: number
    }>,
    fetchImpl: typeof fetch = activeFetch,
  ): Promise<boolean> {
    if (next.mode !== undefined) graphMode.value = next.mode
    if (next.statuses !== undefined) graphStatuses.value = [...next.statuses]
    if (next.focus !== undefined) graphFocus.value = next.focus.trim()
    if (next.page !== undefined) graphPage.value = Math.max(1, next.page)
    if (graphFocus.value || graphMode.value === 'history') graphStatuses.value = []
    return pollGraph(fetchImpl)
  }

  async function pollTimeline(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const ref = selectedTaskRef.value
    if (!ref) {
      resetTimeline()
      return true
    }
    const ok = await request<DeliveryTimelineResponse, DeliveryTimelineResponse | null>(
      timelineSurface,
      `/api/delivery-timeline?task=${encodeURIComponent(ref)}`,
      (payload) => payload,
      fetchImpl,
    )
    return ok
  }

  const pollAttention = (fetchImpl: typeof fetch = activeFetch) =>
    request<OperatorAttentionResponse, OperatorAttentionResponse | null>(
      attentionSurface,
      '/api/attention',
      (payload) => payload,
      fetchImpl,
    )

  function selectedTaskID(ref: string): string {
    return (
      tasksSurface.value.data.find(
        (task) => task.id === ref || String(task.seq) === ref || task.slug === ref,
      )?.id ?? ref
    )
  }

  async function pollTaskDetail(fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    const ref = selectedTaskRef.value
    if (!ref) {
      resetTaskInspection()
      return true
    }
    const expected = selectedTaskID(ref)
    const [detailOK, eventsOK] = await Promise.all([
      request<TaskDetailResponse, TaskDetail | null>(
        taskDetailSurface,
        `/api/task?ref=${encodeURIComponent(ref)}`,
        (payload) => {
          if (!payload.task || payload.task.id !== expected) {
            throw new Error(`task identity mismatch: requested ${expected}`)
          }
          return payload.task
        },
        fetchImpl,
      ),
      request<TaskEventsResponse, TaskEventsResponse | null>(
        taskEventsSurface,
        `/api/events?task=${encodeURIComponent(ref)}`,
        (payload) => {
          if (payload.task !== expected) {
            throw new Error(`task event identity mismatch: requested ${expected}`)
          }
          return payload
        },
        fetchImpl,
      ),
    ])
    if (detailOK && taskDetailSurface.value.data) {
      taskDetailCache.set(expected, {
        data: taskDetailSurface.value.data,
        generated: taskDetailSurface.value.generated,
      })
    }
    if (eventsOK && taskEventsSurface.value.data) {
      taskEventsCache.set(expected, {
        data: taskEventsSurface.value.data,
        generated: taskEventsSurface.value.generated,
      })
    }
    return detailOK && eventsOK
  }

  function resetTimeline(): void {
    timelineSurface.value.controller?.abort()
    timelineSurface.value.generation++
    timelineSurface.value.controller = null
    timelineSurface.value.data = null
    timelineSurface.value.phase = 'loading'
    timelineSurface.value.error = null
    timelineSurface.value.status = null
    timelineSurface.value.generated = null
    timelineSurface.value.lastOk = null
  }

  function resetTaskInspection(): void {
    for (const target of [taskDetailSurface, taskEventsSurface]) {
      target.value.controller?.abort()
      target.value.generation++
      target.value.controller = null
      target.value.data = null
      target.value.phase = 'loading'
      target.value.error = null
      target.value.status = null
      target.value.generated = null
      target.value.lastOk = null
    }
  }

  function resetTasks(): void {
    tasksSurface.value.controller?.abort()
    tasksSurface.value.generation++
    tasksSurface.value.controller = null
    tasksSurface.value.data = []
    tasksSurface.value.phase = 'loading'
    tasksSurface.value.error = null
    tasksSurface.value.status = null
    tasksSurface.value.generated = null
    tasksSurface.value.lastOk = null
  }

  function selectTask(ref: string, fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    if (
      ref === selectedTaskRef.value &&
      (taskDetailSurface.value.lastOk !== null || timelineSurface.value.lastOk !== null)
    ) {
      return Promise.resolve(true)
    }
    selectedTaskRef.value = ref
    resetTimeline()
    resetTaskInspection()
    if (!ref) return Promise.resolve(true)
    const expected = selectedTaskID(ref)
    const cachedDetail = taskDetailCache.get(expected)
    const cachedEvents = taskEventsCache.get(expected)
    if (cachedDetail && cachedEvents) {
      taskDetailSurface.value.data = cachedDetail.data
      taskDetailSurface.value.generated = cachedDetail.generated
      taskDetailSurface.value.lastOk = Date.now()
      taskDetailSurface.value.phase = 'live'
      taskEventsSurface.value.data = cachedEvents.data
      taskEventsSurface.value.generated = cachedEvents.generated
      taskEventsSurface.value.lastOk = Date.now()
      taskEventsSurface.value.phase = 'live'
    }
    if (activeRoute.value === 'delivery') {
      return pollTimeline(fetchImpl)
    }
    if (activeRoute.value === 'work') return pollTaskDetail(fetchImpl)
    return Promise.resolve(true)
  }

  function resetGraph(): void {
    graphSurface.value.controller?.abort()
    graphSurface.value.generation++
    graphSurface.value.controller = null
    graphSurface.value.data = emptyGraph()
    graphSurface.value.phase = 'loading'
    graphSurface.value.error = null
    graphSurface.value.status = null
    graphSurface.value.generated = null
    graphSurface.value.lastOk = null
  }

  function selectProject(slug: string, fetchImpl: typeof fetch = activeFetch): Promise<boolean> {
    if (slug === selectedSlug.value) return Promise.resolve(true)
    selectedSlug.value = slug
    selectedTaskRef.value = ''
    graphMode.value = 'operational'
    graphStatuses.value = []
    graphFocus.value = ''
    graphPage.value = 1
    resetGraph()
    resetTasks()
    resetTaskInspection()
    resetOperation()
    resetOutcomes()
    if (slug && activeRoute.value === 'delivery') return pollGraph(fetchImpl)
    if (slug && activeRoute.value === 'work') return pollTasks(fetchImpl)
    if (slug && activeRoute.value === 'agents')
      return Promise.all([pollOperation(fetchImpl), pollOutcomes(fetchImpl)]).then((results) =>
        results.every(Boolean),
      )
    return Promise.resolve(true)
  }

  function schedule(
    name: SurfaceName,
    interval: number,
    poll: (fetchImpl?: typeof fetch) => Promise<boolean>,
  ): void {
    const loop = async () => {
      if (!running || !activeSurfaces.has(name)) return
      await poll(activeFetch)
      if (running && !paused.value && activeSurfaces.has(name)) {
        timers.set(name, setTimeout(loop, interval))
      }
    }
    void loop()
  }

  function startSurface(name: SurfaceName): void {
    switch (name) {
      case 'overview':
        schedule(name, FAST_POLL_MS, pollOverview)
        break
      case 'projects':
        schedule(name, SLOW_POLL_MS, pollProjects)
        break
      case 'tasks':
        schedule(name, SLOW_POLL_MS, pollTasks)
        break
      case 'agents':
        schedule(name, FAST_POLL_MS, pollAgents)
        break
      case 'burn':
        schedule(name, SLOW_POLL_MS, pollBurn)
        break
      case 'operations':
        schedule(name, SLOW_POLL_MS, pollOperation)
        break
      case 'outcomes':
        schedule(name, SLOW_POLL_MS, pollOutcomes)
        break
      case 'roles':
        schedule(name, ROSTER_POLL_MS, pollRoles)
        break
      case 'graph':
        schedule(name, SLOW_POLL_MS, async (fetchImpl = activeFetch) => {
          if (!selectedSlug.value) return true
          return pollGraph(fetchImpl)
        })
        break
      case 'timeline':
        schedule(name, FAST_POLL_MS, pollTimeline)
        break
      case 'attention':
        schedule(name, SLOW_POLL_MS, pollAttention)
        break
      case 'events':
        schedule(name, SLOW_POLL_MS, pollActivity)
        break
    }
  }

  function abortSurface(name: SurfaceName): void {
    const timer = timers.get(name)
    if (timer) clearTimeout(timer)
    timers.delete(name)
    const target = {
      overview: overviewSurface,
      projects: projectsSurface,
      tasks: tasksSurface,
      agents: agentsSurface,
      burn: burnSurface,
      operations: operationSurface,
      outcomes: outcomeSurface,
      roles: rolesSurface,
      graph: graphSurface,
      timeline: timelineSurface,
      attention: attentionSurface,
      events: activitySurface,
    }[name]
    target.value.controller?.abort()
    target.value.controller = null
    target.value.generation++
  }

  function activateRoute(route: DashboardRouteName): void {
    activeRoute.value = route
    routeGeneration.value++
    const next = new Set<SurfaceName>(ROUTE_SURFACES[route])
    for (const name of activeSurfaces) if (!next.has(name)) abortSurface(name)
    const previous = activeSurfaces
    activeSurfaces = next
    if (running && !paused.value) {
      for (const name of next) if (!previous.has(name)) startSurface(name)
    }
  }

  function start(fetchImpl: typeof fetch = fetch, route: DashboardRouteName = 'overview'): void {
    if (running) return
    running = true
    activeFetch = fetchImpl
    activeRoute.value = route
    routeGeneration.value++
    activeSurfaces = new Set(ROUTE_SURFACES[route])
    // A paused deep link still needs one honest observation after reload. The
    // scheduler performs that first read but does not arm another timer.
    for (const name of activeSurfaces) startSurface(name)
  }

  function setPaused(next: boolean): void {
    if (paused.value === next) return
    paused.value = next
    if (!running) return
    if (next) {
      for (const name of activeSurfaces) abortSurface(name)
      return
    }
    for (const name of activeSurfaces) startSurface(name)
  }

  function stop(): void {
    running = false
    for (const name of SURFACE_NAMES) abortSurface(name)
    resetAgentDetail()
    resetTaskInspection()
  }

  async function retryCurrent(fetchImpl: typeof fetch = activeFetch): Promise<void> {
    const graphGeneration = graphSurface.value.generation
    const requests: Promise<boolean>[] = []
    for (const name of activeSurfaces) {
      if (name === 'overview') requests.push(pollOverview(fetchImpl))
      if (name === 'projects') requests.push(pollProjects(fetchImpl))
      if (name === 'tasks') requests.push(pollTasks(fetchImpl))
      if (name === 'agents') requests.push(pollAgents(fetchImpl))
      if (name === 'burn') requests.push(pollBurn(fetchImpl))
      if (name === 'operations') requests.push(pollOperation(fetchImpl))
      if (name === 'outcomes') requests.push(pollOutcomes(fetchImpl))
      if (name === 'roles') requests.push(pollRoles(fetchImpl))
      if (name === 'timeline') requests.push(pollTimeline(fetchImpl))
      if (name === 'attention') requests.push(pollAttention(fetchImpl))
      if (name === 'events') requests.push(pollActivity(fetchImpl))
    }
    await Promise.all(requests)
    if (
      activeSurfaces.has('graph') &&
      selectedSlug.value &&
      graphSurface.value.generation === graphGeneration
    ) {
      await pollGraph(fetchImpl)
    }
  }

  async function retryAll(fetchImpl: typeof fetch = activeFetch): Promise<void> {
    await Promise.all([
      pollOverview(fetchImpl),
      pollProjects(fetchImpl),
      pollAgents(fetchImpl),
      pollBurn(fetchImpl),
      pollRoles(fetchImpl),
      pollAttention(fetchImpl),
    ])
    if (selectedSlug.value) await Promise.all([pollGraph(fetchImpl), pollOperation(fetchImpl)])
  }

  const projects = computed(() =>
    projectsSurface.value.data.map((project) => ({
      ...project,
      graph:
        project.slug === selectedSlug.value && graphSurface.value.data.project === project.slug
          ? graphSurface.value.data
          : emptyGraph(),
    })),
  )
  const agents = computed(() => agentsSurface.value.data)
  const roles = computed(() => rolesSurface.value.data)
  const burn = computed(() => burnSurface.value.data)
  const pendingEvents = computed(() => overviewSurface.value.data?.pending_events ?? 0)

  const surfaces = computed<SurfaceStatus[]>(() => {
    // The Set is intentionally private and mutable; this reactive counter makes
    // route changes invalidate the aggregate status without exposing policy as
    // client-editable state.
    void routeGeneration.value
    const all: Record<SurfaceName, SurfaceStatus> = {
      overview: overviewSurface.value,
      projects: projectsSurface.value,
      tasks: tasksSurface.value,
      agents: agentsSurface.value,
      burn: burnSurface.value,
      operations: operationSurface.value,
      outcomes: outcomeSurface.value,
      roles: rolesSurface.value,
      graph: graphSurface.value,
      timeline: timelineSurface.value,
      attention: attentionSurface.value,
      events: activitySurface.value,
    }
    return [...activeSurfaces].map((name) => all[name])
  })
  const phase = computed<Phase>(() => {
    const current = surfaces.value
    const snapshots = current.filter((item) => item.lastOk !== null).length
    const errors = current.filter((item) => item.phase === 'error').length
    const loading = current.filter((item) => item.phase === 'loading').length
    if (snapshots === 0 && errors > 0 && loading === 0) return 'error'
    if (snapshots === 0) return 'loading'
    if (errors > 0 || loading > 0) return 'partial'
    return 'live'
  })
  const error = computed(() => {
    void routeGeneration.value
    const failed: string[] = []
    const named: Array<[SurfaceName, Surface<unknown>]> = [
      ['overview', overviewSurface.value],
      ['projects', projectsSurface.value],
      ['tasks', tasksSurface.value],
      ['agents', agentsSurface.value],
      ['burn', burnSurface.value],
      ['operations', operationSurface.value],
      ['outcomes', outcomeSurface.value],
      ['roles', rolesSurface.value],
      ['graph', graphSurface.value],
      ['timeline', timelineSurface.value],
      ['attention', attentionSurface.value],
      ['events', activitySurface.value],
    ]
    for (const [name, value] of named) {
      if (activeSurfaces.has(name) && value.error) failed.push(`${name}: ${value.error}`)
    }
    return failed.join('; ') || null
  })
  const generated = computed(() => {
    const stamps = surfaces.value
      .map((item) => item.generated)
      .filter((stamp): stamp is string => Boolean(stamp))
      .sort()
    return stamps.length > 0 ? stamps[stamps.length - 1] : null
  })

  return {
    overviewSurface,
    projectsSurface,
    tasksSurface,
    agentsSurface,
    burnSurface,
    operationSurface,
    outcomeSurface,
    outcomeRange,
    rolesSurface,
    graphSurface,
    graphMode,
    graphStatuses,
    graphFocus,
    graphPage,
    timelineSurface,
    attentionSurface,
    activitySurface,
    activityQuery,
    taskDetailSurface,
    taskEventsSurface,
    agentDetailSurface,
    selectedSlug,
    selectedTaskRef,
    selectedAgentID,
    paused,
    projects,
    agents,
    roles,
    burn,
    pendingEvents,
    phase,
    error,
    generated,
    pollOverview,
    pollProjects,
    pollTasks,
    pollAgents,
    pollAgentDetail,
    pollBurn,
    pollOperation,
    pollOutcomes,
    setOutcomeRange,
    pollRoles,
    pollGraph,
    setGraphQuery,
    pollTimeline,
    pollAttention,
    pollActivity,
    configureActivity,
    pollTaskDetail,
    selectProject,
    selectTask,
    selectAgent,
    activateRoute,
    setPaused,
    start,
    stop,
    retryCurrent,
    retryAll,
  }
})
