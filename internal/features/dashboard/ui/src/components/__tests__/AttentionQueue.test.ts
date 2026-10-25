import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import AttentionQueue from '../AttentionQueue.vue'
import type { NativeSnapshot, OperatorAttentionItem, OperatorAttentionResponse } from '@/types'

function response(): OperatorAttentionResponse {
  return {
    schema: 'operator-attention/v1',
    generated: '2026-09-02T12:00:00Z',
    alerts: [
      {
        id: 'github_state_unknown/core/task-1//',
        code: 'github_state_unknown',
        severity: 'critical',
        affected: { project: 'core', task: 'task-1', pr: 'https://github.test/pull/1' },
        first_observed: '2026-09-02T09:00:00Z',
        last_observed: '2026-09-02T11:00:00Z',
        freshness: 'stale',
        retryable: true,
        summary: 'GitHub state is unknown; absence is not healthy.',
        next_action: 'Re-observe the exact head after GitHub recovers.',
        link: '#/delivery?project=core&task=task-1',
        evidence: [
          {
            kind: 'github-check',
            id: 'check-99',
            url: 'https://github.test/check/99',
            observed_at: '2026-09-02T11:00:00Z',
            confidence: 'low',
          },
        ],
        occurrences: 3,
        duration_seconds: 7200,
        critical_path: true,
        confidence: 'low',
        rank: 1,
        rank_reason: 'severity=critical; critical_path=true; age=3h; confidence=low',
      },
    ],
    ranking_rule: 'severity, critical path, age, confidence, stable identity',
  }
}

function nativeAlert(
  category: string,
  task: string,
  freshness: OperatorAttentionItem['freshness'],
  rank: number,
): OperatorAttentionItem {
  const link = `#/work?project=core&task=${task}`
  return {
    id: `native_${category}/core/${task}//`,
    code: `native_${category}`,
    severity: 'high',
    affected: { project: 'core', task },
    first_observed: '2026-10-05T08:00:00Z',
    last_observed: '2026-10-05T09:00:00Z',
    freshness,
    retryable: false,
    summary: `${category} native condition for ${task}`,
    next_action: 'Inspect the exact native task record.',
    link,
    evidence: [
      {
        kind: 'native-governance',
        id: `record-${task}`,
        url: link,
        observed_at: '2026-10-05T09:00:00Z',
        confidence: 'high',
      },
    ],
    occurrences: 1,
    duration_seconds: 3600,
    critical_path: false,
    confidence: 'high',
    rank,
    rank_reason: `native category=${category}; freshness=${freshness}`,
  }
}

function nativeSnapshot(): NativeSnapshot {
  return {
    schema: 'native-dashboard/v1',
    state: 'ready',
    project: 'core',
    observed_at: '2026-10-05T09:05:00Z',
    authority_version: 16,
    limited: false,
    services: { preflight: 'supported', budget: 'supported' },
    records: [],
    attention: [],
    spend: {
      state: 'estimated',
      provenance: 'owner-estimate',
      enforcement: 'local allocation only',
      unit: 'owner-estimated tokens',
      estimated_spent: 0,
      estimated_reserved: 40,
      estimated_limit: 100,
      provider_tokens: null,
      provider_cost_usd: null,
      unknown_retained: true,
    },
  }
}

describe('AttentionQueue', () => {
  it('renders ranked policy evidence and exact drill-downs without color-only meaning', () => {
    const wrapper = mount(AttentionQueue, {
      props: { attention: response(), phase: 'live', hasSnapshot: true, error: null },
    })
    expect(wrapper.text()).toContain('critical severity')
    expect(wrapper.text()).toContain('critical path')
    expect(wrapper.text()).toContain('stale evidence')
    expect(wrapper.text()).toContain('Occurrences3')
    expect(wrapper.text()).toContain('Why this is rank 1')
    expect(wrapper.get('a[href="https://github.test/check/99"]').text()).toContain('check-99')
    expect(wrapper.get('a[href="#/delivery?project=core&task=task-1"]').text()).toContain(
      'Inspect exact evidence',
    )
    expect(wrapper.get('ol').attributes('aria-label')).toBe('Prioritized operator alerts')
  })

  it('keeps the last snapshot explicitly stale after refresh failure', () => {
    const wrapper = mount(AttentionQueue, {
      props: { attention: response(), phase: 'error', hasSnapshot: true, error: 'HTTP 503' },
    })
    expect(wrapper.get('[role="status"]').text()).toContain('last successful queue snapshot')
    expect(wrapper.text()).toContain('HTTP 503')
    expect(wrapper.text()).toContain('github state unknown')
  })

  it('is responsive at 390px and preserves the same keyboard links', () => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 390 })
    const wrapper = mount(AttentionQueue, {
      props: { attention: response(), phase: 'live', hasSnapshot: true, error: null },
    })
    expect(wrapper.get('ol').classes()).toContain('grid')
    expect(wrapper.findAll('a')).toHaveLength(2)
  })

  it('does not claim missing external state is healthy when the queue is empty', () => {
    const empty = response()
    empty.alerts = []
    const wrapper = mount(AttentionQueue, {
      props: { attention: empty, phase: 'live', hasSnapshot: true, error: null },
    })
    expect(wrapper.text()).toContain('current-state observation')
    expect(wrapper.text()).toContain('not a claim that missing external systems are healthy')
  })

  it('renders native governance snapshots and links each freshness-labeled condition to its exact Work task', () => {
    const categories = [
      ['blocked', 'task-blocked', 'stale'],
      ['disconnected', 'task-disconnected', 'fresh'],
      ['approval-needed', 'task-approval', 'stale'],
      ['unsupported-capability', 'task-unsupported', 'missing'],
    ] as const
    const nativeResponse = response()
    nativeResponse.native = [nativeSnapshot()]
    nativeResponse.alerts = categories.map(([category, task, freshness], index) =>
      nativeAlert(category, task, freshness, index + 1),
    )

    const wrapper = mount(AttentionQueue, {
      props: { attention: nativeResponse, phase: 'live', hasSnapshot: true, error: null },
    })

    expect(wrapper.findAll('[aria-label="Native governance"]')).toHaveLength(1)
    const nativePanel = wrapper.get('[aria-label="Native governance"]')
    expect(nativePanel.text()).toContain('Native governance · core')
    expect(nativePanel.text()).toContain('Provider tokens (measured)Unavailable')
    expect(nativePanel.text()).toContain('Provider cost USD (measured)Unavailable')
    expect(nativePanel.text()).toContain('Owner estimate spent0')
    expect(nativePanel.text()).toContain('Owner estimate reserved40')

    for (const [index, [category, task, freshness]] of categories.entries()) {
      const alertCard = wrapper.get(`[aria-labelledby="attention-${index + 1}"]`)
      expect(alertCard.text()).toContain(`native ${category}`)
      expect(alertCard.text()).toContain(`${freshness} evidence`)
      const taskLinks = alertCard.findAll(`a[href="#/work?project=core&task=${task}"]`)
      expect(taskLinks).toHaveLength(2)
      expect(taskLinks[0].text()).toContain(`record-${task}`)
      expect(taskLinks[1].text()).toContain('Inspect exact evidence')
    }
  })
})
