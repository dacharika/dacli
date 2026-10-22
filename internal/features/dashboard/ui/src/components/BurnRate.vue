<script setup lang="ts">
import NativeGovernancePanel from '@/components/NativeGovernancePanel.vue'
import { computed } from 'vue'
import ChartFrame from '@/components/ChartFrame.vue'
import type { Burn, ChartContract } from '@/types'
import { Badge } from '@/components/ui/badge'

// The burn-rate surface (task 144). Unlike a passive burndown line, this chart
// YELLS: when the current burn rate reaches `alert_at`× the calibrated ceiling
// the whole panel turns danger-red and raises an assertive live-region banner —
// the one signal all four discovery segments asked for, to catch overspend
// before it becomes a silent, expensive failure. Data-only prop, like the other
// chart leaves; the store owns the poll and hands down the latest `burn`.
const props = defineProps<{
  burn: Burn
  project?: string
  generated?: string | null
  stale?: boolean
  focusDay?: string
}>()

const hasSeries = computed(() => props.burn.series.length > 0)

/** Scale bars and the ceiling line against the tallest of the two, so the
 * ceiling is always on-canvas even when every bar sits below it. 0-safe. */
const scaleMax = computed(() => {
  const peak = props.burn.series.reduce((m, p) => Math.max(m, p.per_run), 0)
  return Math.max(peak, props.burn.ceiling)
})

/** Bar height as a percentage of the scale; a 4% floor keeps a tiny non-zero
 * day visible, and an all-zero series collapses to 0 (no divide by zero). */
function barPct(perRun: number): number {
  if (scaleMax.value <= 0) return 0
  return Math.max(4, (perRun / scaleMax.value) * 100)
}

/** Where the ceiling reference line sits, as a percentage from the bottom.
 * Hidden (0) when there is no calibrated ceiling to draw. */
const ceilingPct = computed(() =>
  scaleMax.value > 0 ? (props.burn.ceiling / scaleMax.value) * 100 : 0,
)

/** A single day is "hot" when it alone breaches the alert threshold — that bar
 * is painted danger even if the headline rate (the latest day) has cooled. */
function isHot(perRun: number): boolean {
  return props.burn.ceiling > 0 && perRun >= props.burn.alert_at * props.burn.ceiling
}

const ratioText = computed(() => props.burn.ratio.toFixed(1))
const rateText = computed(() => Math.round(props.burn.rate).toLocaleString())
const ceilingText = computed(() => Math.round(props.burn.ceiling).toLocaleString())
const totalTokens = computed(() =>
  props.burn.series.reduce((total, point) => total + point.tokens, 0).toLocaleString(),
)
const totalCost = computed(() =>
  props.burn.series
    .reduce((total, point) => total + point.cost_usd, 0)
    .toLocaleString(undefined, {
      style: 'currency',
      currency: 'USD',
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    }),
)

const alertMessage = computed(
  () =>
    `burning ${ratioText.value}× the calibrated ceiling of ${ceilingText.value} ${props.burn.unit}/run`,
)
const selectedPoint = computed(() =>
  props.burn.series.find((point) => point.day === props.focusDay),
)
const chartContract = computed<ChartContract>(() => ({
  id: 'burn-intensity',
  title: 'Daily run intensity',
  metric_definition: 'Provider-reported output tokens per implementation run',
  unit: `${props.burn.unit}/run`,
  window: props.burn.series.length
    ? `${props.burn.series[0].day} → ${props.burn.series[props.burn.series.length - 1].day}`
    : 'no observed window',
  source: 'run usage.txt + calibrated role/model/runtime bands',
  freshness: props.stale
    ? `stale since ${props.generated ?? 'unknown'}`
    : (props.generated ?? 'current local observation'),
  coverage: `${props.burn.series.reduce((sum, point) => sum + point.runs, 0)} usage-bearing run(s)`,
  comparison:
    props.burn.ceiling > 0
      ? `latest rate ÷ calibrated ceiling = ${ratioText.value}×`
      : 'calibrated comparison unavailable',
  state: props.stale
    ? 'stale'
    : !hasSeries.value
      ? 'empty'
      : props.burn.bands.some((band) => !band.calibrated)
        ? 'partial'
        : 'live',
  state_detail: props.stale
    ? 'The last good burn series is retained after a failed refresh.'
    : 'Some calibration bands remain provisional; exact samples are shown below.',
  summary: hasSeries.value
    ? `${totalTokens.value} provider-reported output tokens across ${props.burn.series.length} UTC day points. ${props.burn.alert ? alertMessage.value : 'Latest intensity is below the recorded alert threshold.'}`
    : 'No provider usage has been recorded.',
  points: props.burn.series.map((point) => ({
    id: point.day,
    label: point.day,
    value: point.per_run,
    display: `${Math.round(point.per_run).toLocaleString()} ${props.burn.unit}/run · ${point.runs} run(s)`,
    href: props.project
      ? `#/agents?project=${encodeURIComponent(props.project)}&burn_day=${point.day}`
      : undefined,
    evidence_count: point.run_ids?.length ?? 0,
  })),
  hidden_resolution:
    (props.burn.hidden_points ?? 0) > 0
      ? `${props.burn.hidden_points ?? 0} intermediate day point(s) hidden; first, last, minimum, and maximum retained`
      : undefined,
}))
</script>

<template>
  <section
    aria-labelledby="burn-h"
    class="rounded-lg border bg-card px-4 py-3.5 transition-colors"
    :class="
      burn.alert
        ? 'alert border-destructive shadow-[0_0_0_1px_var(--destructive)]'
        : 'border-border'
    "
  >
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2
        id="burn-h"
        class="m-0 text-[13px] font-semibold uppercase tracking-[0.06em] text-muted-foreground"
      >
        Burn rate
      </h2>
      <Badge
        v-if="burn.ceiling > 0"
        :variant="burn.alert ? 'destructive' : 'outline'"
        class="font-normal"
        :class="{ 'font-semibold': burn.alert }"
        :title="`current rate ÷ calibrated ceiling`"
      >
        {{ ratioText }}× ceiling
      </Badge>
    </div>

    <!-- The yell: an assertive live region only present when overspend trips. -->
    <p
      v-if="burn.alert"
      class="mt-2.5 mb-0 text-[13px] font-semibold text-destructive"
      role="alert"
      aria-live="assertive"
    >
      <span aria-hidden="true">⚠ </span>{{ alertMessage }}
    </p>

    <p v-if="!hasSeries" class="mt-2.5 mb-0 text-xs text-muted-foreground">
      no usage recorded yet — burn starts when a run reports tokens
    </p>

    <template v-else>
      <div class="mt-3 mb-2.5 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <span
          class="burn-metric flex min-w-0 flex-col items-start gap-0.5 sm:flex-row sm:items-baseline sm:gap-1.5"
        >
          <span class="text-[10px] font-semibold uppercase tracking-[0.05em] text-muted-foreground"
            >rate</span
          >
          <span
            class="text-xl font-semibold tabular-nums"
            :class="{ 'text-destructive': burn.alert }"
            >{{ rateText }}</span
          >
          <span
            class="burn-unit max-w-full break-all text-[11px] text-muted-foreground sm:break-normal"
            >{{ burn.unit }}/run</span
          >
        </span>
        <span
          class="burn-metric flex min-w-0 flex-col items-start gap-0.5 sm:flex-row sm:items-baseline sm:gap-1.5"
        >
          <span class="text-[10px] font-semibold uppercase tracking-[0.05em] text-muted-foreground"
            >actual</span
          >
          <span class="text-xl font-semibold tabular-nums">{{ totalTokens }}</span>
          <span class="text-[11px] text-muted-foreground">tokens</span>
        </span>
        <span
          class="burn-metric flex min-w-0 flex-col items-start gap-0.5 sm:flex-row sm:items-baseline sm:gap-1.5"
        >
          <span class="text-[10px] font-semibold uppercase tracking-[0.05em] text-muted-foreground"
            >cost</span
          >
          <span class="text-xl font-semibold tabular-nums">{{ totalCost }}</span>
        </span>
        <span
          v-if="burn.ceiling > 0"
          class="burn-metric flex min-w-0 flex-col items-start gap-0.5 sm:flex-row sm:items-baseline sm:gap-1.5"
        >
          <span class="text-[10px] font-semibold uppercase tracking-[0.05em] text-muted-foreground"
            >ceiling</span
          >
          <span class="text-xl font-semibold tabular-nums">{{ ceilingText }}</span>
          <span
            class="burn-unit max-w-full break-all text-[11px] text-muted-foreground sm:break-normal"
            >{{ burn.unit }}/run</span
          >
        </span>
      </div>

      <ChartFrame :contract="chartContract">
        <div class="relative flex h-16 items-end gap-1" role="list" aria-label="Daily burn points">
          <div
            v-if="ceilingPct > 0"
            class="ceiling-line pointer-events-none absolute right-0 left-0 h-0 border-t border-dashed border-muted-foreground"
            :style="{ bottom: ceilingPct + '%' }"
            aria-hidden="true"
          />
          <a
            v-for="d in burn.series"
            :key="d.day"
            :href="
              project
                ? `#/agents?project=${encodeURIComponent(project)}&burn_day=${d.day}`
                : undefined
            "
            class="flex h-full min-w-1 flex-1 items-end rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            role="listitem"
            :aria-label="`${d.day}: ${Math.round(d.per_run).toLocaleString()} ${burn.unit} per run, ${d.runs} runs; open ${d.run_ids?.length ?? 0} exact run records`"
          >
            <span
              class="bar min-h-0.5 w-full rounded-t-sm"
              :class="isHot(d.per_run) ? 'hot bg-destructive' : 'bg-primary'"
              :style="{ height: barPct(d.per_run) + '%' }"
              :title="`${d.day}: ${Math.round(d.per_run).toLocaleString()} ${burn.unit}/run · ${d.runs} run(s)`"
            />
          </a>
        </div>
        <p class="mt-2 mb-0 text-[11px] text-muted-foreground">
          {{ burn.series[burn.series.length - 1].day }} · latest of {{ burn.series.length }} day(s),
          ceiling from {{ burn.bands.length }} calibrated band(s)
        </p>
      </ChartFrame>

      <div v-if="selectedPoint" class="mt-3 rounded-md border border-border bg-muted/30 p-3">
        <p class="m-0 text-xs font-semibold">{{ selectedPoint.day }} exact runs</p>
        <div class="mt-2 flex flex-wrap gap-1.5">
          <a
            v-for="run in selectedPoint.run_ids ?? []"
            :key="run"
            :href="`/api/agents/transcript?run=${encodeURIComponent(run)}`"
            class="rounded bg-background px-2 py-1 font-mono text-[10px] text-primary hover:underline"
            >{{ run }}</a
          ><span
            v-if="(selectedPoint.run_ids?.length ?? 0) === 0"
            class="text-xs text-muted-foreground"
            >No exact run identity was retained for this historical point.</span
          >
        </div>
      </div>

      <div class="mt-3 grid gap-3 border-t border-border pt-3 md:grid-cols-2">
        <div>
          <h3
            class="m-0 text-[10px] font-semibold uppercase tracking-[0.06em] text-muted-foreground"
          >
            Calibrated bands
          </h3>
          <ul class="mt-2 mb-0 list-none space-y-1 p-0 text-[11px]">
            <li
              v-for="band in burn.bands"
              :key="band.band"
              class="flex items-center justify-between gap-3"
            >
              <span class="truncate">{{ band.band }}</span>
              <span class="shrink-0 font-mono"
                >{{ Math.round(band.expected).toLocaleString() }} · n={{ band.n }} ·
                {{ band.calibrated ? 'calibrated' : 'provisional' }}</span
              >
            </li>
            <li v-if="burn.bands.length === 0" class="text-muted-foreground">
              No calibrated role/model/runtime band yet.
            </li>
          </ul>
        </div>
        <div>
          <h3
            class="m-0 text-[10px] font-semibold uppercase tracking-[0.06em] text-muted-foreground"
          >
            Governor windows
          </h3>
          <ul class="mt-2 mb-0 list-none space-y-1 p-0 text-[11px]">
            <li
              v-for="window in burn.windows"
              :key="window.project"
              class="flex items-center justify-between gap-3"
            >
              <span>{{ window.project }}</span
              ><span class="font-mono"
                >{{ window.spent.toLocaleString() }} spent ·
                {{ window.start || 'not started' }}</span
              >
            </li>
            <li v-if="burn.windows.length === 0" class="text-muted-foreground">
              No persisted governor window.
            </li>
          </ul>
        </div>
      </div>
    </template>
    <NativeGovernancePanel
      v-for="snapshot in burn.native ?? []"
      :key="snapshot.project"
      :snapshot="snapshot"
    />
  </section>
</template>
