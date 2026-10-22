<script setup lang="ts">
import NativeGovernancePanel from '@/components/NativeGovernancePanel.vue'
import { computed } from 'vue'
import {
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from 'reka-ui'
import type { Phase, TaskDetail, TaskEventsResponse } from '@/types'
import SkeletonBlock from '@/components/SkeletonBlock.vue'

const props = defineProps<{
  open: boolean
  selectedRef: string
  task: TaskDetail | null
  phase: Phase
  hasSnapshot: boolean
  error: string | null
  status: number | null
  events: TaskEventsResponse | null
  eventsPhase: Phase
  eventsHasSnapshot: boolean
  eventsError: string | null
}>()
const emit = defineEmits<{
  close: []
  retry: []
  navigateTask: [task: string]
}>()

const failureTitle = computed(() => {
  if (props.status === 400 && props.error?.toLowerCase().includes('ambiguous'))
    return 'Task reference ambiguous'
  if (props.status === 400) return 'Task reference rejected'
  if (props.status === 404) return 'Task record unavailable'
  return 'Task detail unavailable'
})

const acceptancePercent = computed(() => {
  if (!props.task?.acceptance_total) return 0
  return Math.round((props.task.acceptance_done / props.task.acceptance_total) * 100)
})

function onOpenChange(open: boolean): void {
  if (!open) emit('close')
}
</script>

<template>
  <DialogRoot :open="open" @update:open="onOpenChange">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-background/76 backdrop-blur-[2px]" />
      <DialogContent
        class="task-inspector fixed inset-y-0 right-0 z-50 flex w-full max-w-[720px] flex-col border-l border-border bg-card shadow-[-28px_0_90px_-48px_#000] focus:outline-none"
      >
        <header
          class="flex items-start justify-between gap-5 border-b border-border px-5 py-5 sm:px-7"
        >
          <div class="min-w-0">
            <p
              class="m-0 font-mono text-[10px] font-semibold uppercase tracking-[0.16em] text-primary"
            >
              Task evidence record
            </p>
            <DialogTitle class="mt-2 text-xl leading-tight font-semibold tracking-[-0.025em]">
              {{ task?.title ?? selectedRef }}
            </DialogTitle>
            <DialogDescription class="mt-2 font-mono text-xs text-muted-foreground">
              {{ task ? `${task.project} / ${task.id}` : selectedRef }}
            </DialogDescription>
          </div>
          <DialogClose
            aria-label="Close task details"
            class="grid size-11 shrink-0 place-items-center rounded-md border border-border bg-background text-lg text-muted-foreground hover:bg-secondary hover:text-foreground"
          >
            <span aria-hidden="true">×</span>
          </DialogClose>
        </header>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5 sm:px-7">
          <SkeletonBlock
            v-if="phase === 'loading' && !hasSnapshot"
            height="380px"
            aria-label="Loading task detail"
          />

          <section
            v-else-if="phase === 'error' && !hasSnapshot"
            role="alert"
            class="rounded-lg border border-destructive/40 bg-background p-5"
          >
            <h3 class="m-0 text-base font-semibold">{{ failureTitle }}</h3>
            <p class="mt-2 text-sm text-muted-foreground">
              <span class="font-mono text-foreground">{{ selectedRef }}</span> was not replaced with
              another task. {{ error }}
            </p>
            <button
              type="button"
              class="mt-4 min-h-11 rounded-md border border-border px-4 text-xs font-semibold text-primary"
              @click="emit('retry')"
            >
              Retry this task
            </button>
          </section>

          <template v-else-if="task">
            <NativeGovernancePanel :snapshot="task.native" records />
            <div
              v-if="phase === 'error'"
              role="alert"
              class="mb-5 rounded-md border border-warning/40 bg-warning/5 px-4 py-3 text-xs text-warning"
            >
              Detail refresh failed; retained task evidence is stale. {{ error }}
              <button type="button" class="ml-2 underline" @click="emit('retry')">Retry</button>
            </div>

            <section aria-label="Task summary">
              <dl
                class="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-4"
              >
                <div class="bg-background p-3">
                  <dt>Status</dt>
                  <dd>{{ task.status }}</dd>
                </div>
                <div class="bg-background p-3">
                  <dt>Priority</dt>
                  <dd>{{ task.priority || 'normal' }}</dd>
                </div>
                <div class="bg-background p-3">
                  <dt>Owner</dt>
                  <dd class="break-all">{{ task.owner || 'unowned' }}</dd>
                </div>
                <div class="bg-background p-3">
                  <dt>Expected</dt>
                  <dd>{{ task.estimated ? `Te ${task.points}` : 'unestimated' }}</dd>
                </div>
              </dl>
            </section>

            <section aria-labelledby="task-estimate-h" class="mt-7 border-t border-border pt-6">
              <p class="agent-label">Estimate</p>
              <h3 id="task-estimate-h" class="mt-1 text-base font-semibold">Three-point model</h3>
              <p v-if="!task.estimate" class="mt-3 text-sm text-warning">
                No estimate is recorded; zero is not inferred.
              </p>
              <dl v-else class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                <div class="fact">
                  <dt>Optimistic</dt>
                  <dd>{{ task.estimate.optimistic }}</dd>
                </div>
                <div class="fact">
                  <dt>Probable</dt>
                  <dd>{{ task.estimate.probable }}</dd>
                </div>
                <div class="fact">
                  <dt>Pessimistic</dt>
                  <dd>{{ task.estimate.pessimistic }}</dd>
                </div>
                <div class="fact">
                  <dt>Expected</dt>
                  <dd>{{ task.estimate.expected }}</dd>
                </div>
              </dl>
            </section>

            <section aria-labelledby="task-context-h" class="mt-7 border-t border-border pt-6">
              <p class="agent-label">Narrative</p>
              <h3 id="task-context-h" class="mt-1 text-base font-semibold">Intent and context</h3>
              <div class="mt-3 grid gap-3 sm:grid-cols-2">
                <div class="fact">
                  <dt>So that</dt>
                  <dd class="whitespace-pre-wrap">{{ task.so_that || 'not recorded' }}</dd>
                </div>
                <div class="fact">
                  <dt>Context</dt>
                  <dd class="whitespace-pre-wrap">{{ task.context || 'not recorded' }}</dd>
                </div>
              </div>
            </section>

            <section aria-labelledby="task-acceptance-h" class="mt-7 border-t border-border pt-6">
              <p class="agent-label">Acceptance</p>
              <div class="mt-1 flex items-center justify-between gap-3">
                <h3 id="task-acceptance-h" class="text-base font-semibold">
                  {{ task.acceptance_done }} / {{ task.acceptance_total }} checked
                </h3>
                <span class="font-mono text-xs text-muted-foreground"
                  >{{ acceptancePercent }}%</span
                >
              </div>
              <div class="mt-3 h-1.5 overflow-hidden rounded-full bg-secondary" aria-hidden="true">
                <div class="h-full bg-primary" :style="{ width: `${acceptancePercent}%` }" />
              </div>
              <p v-if="task.acceptance.length === 0" class="mt-3 text-sm text-warning">
                No acceptance criteria are recorded.
              </p>
              <ul v-else class="mt-3 space-y-2">
                <li v-for="box in task.acceptance" :key="box.text" class="flex gap-2 text-sm">
                  <span
                    class="font-mono"
                    :class="box.done ? 'text-success' : 'text-muted-foreground'"
                    >{{ box.done ? '✓' : '○' }}</span
                  >
                  <span>{{ box.text }}</span>
                </li>
              </ul>
            </section>

            <section aria-labelledby="task-relations-h" class="mt-7 border-t border-border pt-6">
              <p class="agent-label">Relations</p>
              <h3 id="task-relations-h" class="mt-1 text-base font-semibold">
                Parent and dependencies
              </h3>
              <button
                v-if="task.parent"
                type="button"
                class="task-link mt-3"
                @click="emit('navigateTask', task.parent)"
              >
                Parent · {{ task.parent }}
              </button>
              <p v-else class="mt-3 text-xs text-muted-foreground">No parent is recorded.</p>
              <ul class="mt-3 space-y-2">
                <li v-for="dep in task.deps" :key="`${dep.type}:${dep.ref}`" class="fact">
                  <button
                    v-if="dep.resolved"
                    type="button"
                    class="task-link"
                    @click="emit('navigateTask', dep.id)"
                  >
                    {{ dep.type }} · {{ dep.id }} · {{ dep.title }} · {{ dep.status }}
                  </button>
                  <p v-else class="text-xs text-warning">
                    {{ dep.type }} · {{ dep.ref }} · unresolved dependency
                  </p>
                </li>
              </ul>
              <p v-if="task.deps.length === 0" class="mt-3 text-xs text-muted-foreground">
                No dependencies are recorded.
              </p>
            </section>

            <section aria-labelledby="task-history-h" class="mt-7 border-t border-border pt-6">
              <p class="agent-label">Task log</p>
              <h3 id="task-history-h" class="mt-1 text-base font-semibold">Recorded history</h3>
              <ol class="mt-3 space-y-2">
                <li
                  v-for="entry in [...task.log].reverse()"
                  :key="`${entry.at}:${entry.text}`"
                  class="fact"
                >
                  <time class="font-mono text-[10px] text-muted-foreground">{{
                    entry.at || 'time unrecorded'
                  }}</time>
                  <p class="mt-1 whitespace-pre-wrap text-xs">{{ entry.text }}</p>
                </li>
              </ol>
              <p v-if="task.log.length === 0" class="mt-3 text-xs text-muted-foreground">
                No task log entries are recorded.
              </p>
            </section>

            <section aria-labelledby="task-events-h" class="mt-7 border-t border-border pt-6">
              <p class="agent-label">Task-scoped events</p>
              <h3 id="task-events-h" class="mt-1 text-base font-semibold">Recent durable events</h3>
              <p
                v-if="eventsPhase === 'error' && eventsHasSnapshot"
                role="alert"
                class="mt-3 text-xs text-warning"
              >
                Event refresh failed; retained event evidence is stale. {{ eventsError }}
                <button type="button" class="ml-2 underline" @click="emit('retry')">Retry</button>
              </p>
              <SkeletonBlock
                v-if="eventsPhase === 'loading' && !eventsHasSnapshot"
                class="mt-3"
                height="100px"
              />
              <div
                v-else-if="eventsPhase === 'error' && !eventsHasSnapshot"
                role="alert"
                class="mt-3 fact text-xs text-warning"
              >
                Event history unavailable. {{ eventsError }}
                <button type="button" class="ml-2 underline" @click="emit('retry')">Retry</button>
              </div>
              <template v-else-if="events">
                <p v-if="events.truncated" class="mt-3 text-xs text-warning">
                  Showing the newest {{ events.limit }} events; older history is truncated.
                </p>
                <ol class="mt-3 space-y-2">
                  <li v-for="event in events.events" :key="event.id" class="fact">
                    <div
                      class="flex flex-wrap justify-between gap-2 text-[10px] text-muted-foreground"
                    >
                      <span class="font-mono"
                        >{{ event.kind }} · {{ event.actor || 'unknown actor' }}</span
                      >
                      <span
                        >{{ event.applied ? 'applied' : 'pending' }} ·
                        {{ event.at || 'time unknown' }}</span
                      >
                    </div>
                    <p class="mt-2 whitespace-pre-wrap text-xs">{{ event.body }}</p>
                  </li>
                </ol>
                <p v-if="events.events.length === 0" class="mt-3 text-xs text-muted-foreground">
                  No task-scoped events are recorded.
                </p>
              </template>
            </section>
          </template>
        </div>

        <footer
          class="border-t border-border px-5 py-3 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground sm:px-7"
        >
          projection only · no transition, priority, acceptance, or dependency authority
        </footer>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style scoped>
dt,
.agent-label {
  margin: 0;
  color: hsl(var(--muted-foreground));
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.6rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
dd {
  margin: 0.3rem 0 0;
  font-size: 0.78rem;
  font-weight: 650;
}
.fact {
  border: 1px solid hsl(var(--border));
  border-radius: 0.375rem;
  background: hsl(var(--background));
  padding: 0.75rem;
}
.task-link {
  min-height: 2rem;
  color: hsl(var(--primary));
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.72rem;
  text-align: left;
}
</style>
