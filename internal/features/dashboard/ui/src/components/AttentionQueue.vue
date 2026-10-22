<script setup lang="ts">
import NativeGovernancePanel from '@/components/NativeGovernancePanel.vue'
import { computed } from 'vue'
import type { OperatorAttentionResponse, Phase } from '@/types'

const props = defineProps<{
  attention: OperatorAttentionResponse | null
  phase: Phase
  hasSnapshot: boolean
  error: string | null
}>()
const emit = defineEmits<{ retry: [] }>()

const alerts = computed(() => props.attention?.alerts ?? [])

function severityClass(severity: string): string {
  if (severity === 'critical') return 'border-destructive/50 bg-destructive/5'
  if (severity === 'high') return 'border-warning/50 bg-warning/5'
  return 'border-border bg-card'
}

function duration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}
</script>

<template>
  <section
    aria-labelledby="attention-heading"
    class="overflow-hidden rounded-xl border border-border bg-card"
  >
    <header
      class="flex flex-wrap items-start justify-between gap-3 border-b border-border px-5 py-4"
    >
      <div>
        <p class="m-0 font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-primary">
          Canonical policy state
        </p>
        <h2 id="attention-heading" class="mt-1 mb-0 text-lg font-semibold tracking-[-0.02em]">
          Operator attention
        </h2>
        <p class="mt-1 mb-0 max-w-2xl text-xs leading-relaxed text-muted-foreground">
          Ranked from durable evidence. Items resolve only when their underlying state changes; this
          dashboard cannot dismiss or override them.
        </p>
      </div>
      <span class="rounded-full border border-border bg-background px-3 py-1 font-mono text-xs">
        {{ alerts.length }} open
      </span>
    </header>

    <div v-if="phase === 'loading' && !hasSnapshot" class="p-5" role="status">
      <p class="m-0 text-sm text-muted-foreground">Loading the evidence-linked attention queue…</p>
    </div>
    <div v-else-if="phase === 'error' && !hasSnapshot" class="p-5" role="alert">
      <p class="m-0 text-sm font-semibold">The attention queue could not be observed.</p>
      <p class="mt-1 mb-0 text-xs text-muted-foreground">{{ error }}</p>
      <button
        type="button"
        class="mt-3 min-h-10 rounded-md border border-border bg-background px-3 text-xs font-semibold"
        @click="emit('retry')"
      >
        Retry observation
      </button>
    </div>
    <div v-else-if="alerts.length === 0" class="p-5">
      <p class="m-0 text-sm font-semibold">No canonical condition currently requires attention.</p>
      <p class="mt-1 mb-0 text-xs text-muted-foreground">
        This is a current-state observation, not a claim that missing external systems are healthy.
      </p>
    </div>
    <template v-else>
      <p
        v-if="phase === 'error'"
        class="m-4 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-xs"
        role="status"
      >
        Showing the last successful queue snapshot; refresh failed: {{ error }}
      </p>
      <ol
        class="m-0 grid list-none gap-3 p-4 lg:grid-cols-2"
        aria-label="Prioritized operator alerts"
      >
        <li v-for="alert in alerts" :key="alert.id" class="min-w-0">
          <article
            class="h-full rounded-lg border p-4"
            :class="severityClass(alert.severity)"
            :aria-labelledby="`attention-${alert.rank}`"
          >
            <div
              class="flex flex-wrap items-center gap-2 text-[10px] font-semibold uppercase tracking-[0.08em]"
            >
              <span class="rounded bg-foreground px-2 py-1 text-background"
                >Rank {{ alert.rank }}</span
              >
              <span>{{ alert.severity }} severity</span>
              <span v-if="alert.critical_path" class="rounded border border-current px-2 py-1"
                >critical path</span
              >
              <span>{{ alert.freshness }} evidence</span>
            </div>
            <h3 :id="`attention-${alert.rank}`" class="mt-3 mb-0 text-base font-semibold">
              {{ alert.code.split('_').join(' ') }}
            </h3>
            <p class="mt-1 mb-0 text-sm leading-relaxed">{{ alert.summary }}</p>
            <p class="mt-2 mb-0 font-mono text-[10px] text-muted-foreground">
              {{ alert.affected.project
              }}<template v-if="alert.affected.task"> / {{ alert.affected.task }}</template
              ><template v-if="alert.affected.run"> / {{ alert.affected.run }} </template>
            </p>

            <dl class="mt-3 grid grid-cols-2 gap-2 text-[11px] sm:grid-cols-4">
              <div>
                <dt class="text-muted-foreground">Observed</dt>
                <dd class="m-0 font-medium">{{ duration(alert.duration_seconds) }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">Occurrences</dt>
                <dd class="m-0 font-medium">{{ alert.occurrences }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">Confidence</dt>
                <dd class="m-0 font-medium">{{ alert.confidence }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">Retryable</dt>
                <dd class="m-0 font-medium">{{ alert.retryable ? 'yes' : 'no' }}</dd>
              </div>
            </dl>

            <div class="mt-3 rounded-md border border-border/70 bg-background/70 p-3">
              <p
                class="m-0 text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground"
              >
                Next safe action
              </p>
              <p class="mt-1 mb-0 text-xs leading-relaxed">{{ alert.next_action }}</p>
            </div>
            <details class="mt-3 text-xs">
              <summary class="cursor-pointer font-medium">
                Why this is rank {{ alert.rank }}
              </summary>
              <p class="mt-2 mb-0 text-muted-foreground">{{ alert.rank_reason }}</p>
              <ul class="mt-2 mb-0 list-none space-y-1 p-0">
                <li
                  v-for="evidence in alert.evidence"
                  :key="`${evidence.kind}/${evidence.id}/${evidence.observed_at}`"
                >
                  <a :href="evidence.url" class="font-medium text-primary hover:underline">
                    {{ evidence.kind }} · {{ evidence.id }} · {{ evidence.confidence }} confidence
                  </a>
                </li>
              </ul>
            </details>
            <a
              :href="alert.link"
              class="mt-4 inline-flex min-h-10 items-center rounded-md border border-border bg-background px-3 text-xs font-semibold no-underline hover:bg-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              Inspect exact evidence →
            </a>
          </article>
        </li>
      </ol>
      <p class="border-t border-border px-5 py-3 text-[10px] text-muted-foreground">
        {{ attention?.ranking_rule }}
      </p>
    </template>
    <NativeGovernancePanel
      v-for="snapshot in attention?.native ?? []"
      :key="snapshot.project"
      :snapshot="snapshot"
    />
  </section>
</template>
