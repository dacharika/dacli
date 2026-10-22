<script setup lang="ts">
import { ref } from 'vue'
import type { NativeSnapshot } from '@/types'
const props = defineProps<{ snapshot?: NativeSnapshot; records?: boolean }>()
const root = ref<HTMLElement | null>(null)
function visible(id: string): boolean {
  return props.snapshot?.records.some((record) => record.id === id) ?? false
}
function focusRecord(id: string): void {
  const detail = [...(root.value?.querySelectorAll('details') ?? [])].find(
    (node) => node.id === anchor(id),
  )
  if (detail) {
    detail.open = true
    detail.querySelector('summary')?.focus()
  }
}
function amount(value: number | null | undefined): string {
  return value == null ? 'Unavailable' : value.toLocaleString()
}
function anchor(id: string): string {
  return `native-${encodeURIComponent(id)}`
}
</script>

<template>
  <section
    ref="root"
    aria-label="Native governance"
    class="mt-6 rounded-lg border border-border p-4"
  >
    <h3 class="text-sm font-semibold">
      Native governance · {{ snapshot?.project ?? 'not selected' }}
    </h3>
    <p role="status" class="mt-2 text-xs text-muted-foreground">
      {{ snapshot?.state ?? 'unavailable' }} ·
      {{ snapshot?.observed_at ?? 'No native observation' }}
    </p>
    <p v-if="snapshot?.limited" class="text-xs">
      Limited scoped snapshot; omitted records are not absent evidence.
    </p>
    <p class="mt-2 text-xs">
      Historical records grant no execution, approval or resource release rights.
    </p>
    <dl class="mt-3 grid grid-cols-2 gap-2 text-xs">
      <dt>Provider tokens (measured)</dt>
      <dd>{{ amount(snapshot?.spend.provider_tokens) }}</dd>
      <dt>Provider cost USD (measured)</dt>
      <dd>{{ amount(snapshot?.spend.provider_cost_usd) }}</dd>
      <dt>Owner estimate spent</dt>
      <dd>{{ amount(snapshot?.spend.estimated_spent) }}</dd>
      <dt>Owner estimate reserved</dt>
      <dd>{{ amount(snapshot?.spend.estimated_reserved) }}</dd>
      <dt>Owner estimate limit</dt>
      <dd>{{ amount(snapshot?.spend.estimated_limit) }}</dd>
      <dt>Usage provenance</dt>
      <dd>{{ snapshot?.spend.provenance ?? 'unavailable' }}</dd>
      <dt>Enforcement</dt>
      <dd>{{ snapshot?.spend.enforcement ?? 'unavailable' }}</dd>
    </dl>
    <p v-if="snapshot?.spend.unknown_retained" class="mt-2 text-xs">
      Unknown usage remains reserved; no free headroom is inferred.
    </p>
    <p class="mt-2 text-xs">{{ snapshot?.spend.unit ?? 'Allocation unit unavailable' }}</p>
    <ul v-if="snapshot" aria-label="Native service support" class="mt-3 space-y-1 text-xs">
      <li v-for="(state, service) in snapshot.services" :key="service">
        {{ service }} · {{ state }}
      </li>
    </ul>
    <div v-if="records" class="mt-4">
      <p v-if="!snapshot?.records.length" class="text-xs">No visible native records.</p>
      <details
        v-for="record in snapshot?.records ?? []"
        :id="anchor(record.id)"
        :key="record.kind + record.id"
        class="mt-2 rounded border border-border p-2"
      >
        <summary class="cursor-pointer break-all text-xs">
          {{ record.kind }} · {{ record.id }} · {{ record.state }} · {{ record.freshness }}
        </summary>
        <dl class="mt-2 space-y-1 break-all text-xs">
          <template
            v-for="field in [
              'attempt_id',
              'lease_id',
              'revision_id',
              'reviewer_id',
              'actor_id',
              'decision_id',
            ] as const"
            :key="field"
          >
            <dt v-if="record[field]">{{ field }}</dt>
            <dd v-if="record[field]">
              <button
                v-if="visible(record[field]!)"
                type="button"
                class="underline"
                @click="focusRecord(record[field]!)"
              >
                {{ record[field] }}
              </button>
              <span v-else
                >{{ record[field] }} · linked record unavailable in this scoped snapshot</span
              >
            </dd>
          </template>
          <dt>Reviewed commit / tree</dt>
          <dd>{{ record.commit ?? 'Unavailable' }} / {{ record.tree ?? 'Unavailable' }}</dd>
          <dt>Eligibility</dt>
          <dd>{{ record.eligibility ?? 'Historical identity only' }}</dd>
          <dt>Observed / expires</dt>
          <dd>{{ record.observed_at }} / {{ record.expires_at ?? 'Unavailable' }}</dd>
          <dt>Declared claims</dt>
          <dd>{{ record.claims?.join(', ') || 'None visible' }}</dd>
          <dt>Evidence</dt>
          <dd>
            <span v-for="id in record.evidence_ids ?? []" :key="id" class="mr-2">
              <button v-if="visible(id)" type="button" class="underline" @click="focusRecord(id)">
                {{ id }}
              </button>
              <span v-else>{{ id }} · linked record unavailable in this scoped snapshot</span>
            </span>
          </dd>
          <dt>Provenance / check</dt>
          <dd>{{ record.provenance ?? 'Unavailable' }} / {{ record.check_id ?? 'Unavailable' }}</dd>
        </dl>
      </details>
    </div>
  </section>
</template>
