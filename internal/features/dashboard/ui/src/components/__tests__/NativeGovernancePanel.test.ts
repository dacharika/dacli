import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import NativeGovernancePanel from '../NativeGovernancePanel.vue'
import type { NativeSnapshot } from '@/types'
import { defineComponent, nextTick } from 'vue'
import { useDashboardRoute } from '@/composables/useDashboardRoute'

function snapshot(): NativeSnapshot {
  return {
    schema: 'native-dashboard/v1',
    state: 'ready',
    project: 'core',
    observed_at: '2026-10-05T00:00:00Z',
    authority_version: 16,
    limited: false,
    services: { preflight: 'unsupported' },
    attention: [],
    records: [
      {
        kind: 'review',
        id: 'review-exact',
        task_id: 'task-exact',
        state: 'approve',
        freshness: 'stale',
        observed_at: '2026-10-04T00:00:00Z',
        attempt_id: 'attempt-exact',
        lease_id: 'lease-exact',
        revision_id: 'revision-exact',
        reviewer_id: 'reviewer-independent',
        evidence_ids: ['evidence-exact'],
        commit: 'commit-exact',
        tree: 'tree-exact',
      },
      {
        kind: 'evidence',
        id: 'evidence-exact',
        task_id: 'task-exact',
        state: 'recorded',
        freshness: 'historical',
        observed_at: '2026-10-04T00:00:00Z',
      },
    ],
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
describe('Native governance evidence panel', () => {
  it('keeps unknown measured spend separate from zero owner estimates and preserves route during exact record navigation', async () => {
    window.location.hash = '#/work?task=task-exact'
    const RouteProbe = defineComponent({
      setup() {
        return useDashboardRoute()
      },
      template: '<output>{{ location.name }}:{{ location.selection.task }}</output>',
    })
    const route = mount(RouteProbe)
    const wrapper = mount(NativeGovernancePanel, {
      attachTo: document.body,
      props: { snapshot: snapshot(), records: true },
    })
    expect(
      wrapper
        .findAll('dd')
        .slice(0, 5)
        .map((node) => node.text()),
    ).toEqual(['Unavailable', 'Unavailable', '0', '40', '100'])
    expect(wrapper.text()).toContain('Unknown usage remains reserved')
    expect(wrapper.text()).toContain('stale')
    expect(wrapper.get('[aria-label="Native service support"]').text()).toContain(
      'preflight · unsupported',
    )
    expect(wrapper.text()).toContain('commit-exact / tree-exact')
    expect(wrapper.text()).toContain('reviewer-independent · linked record unavailable')
    const link = wrapper.findAll('button').find((node) => node.text() === 'evidence-exact')!
    await link.trigger('click')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await nextTick()
    expect(window.location.hash).toBe('#/work?task=task-exact')
    expect(route.text()).toBe('work:task-exact')
    const target = wrapper.find('#native-evidence-exact')
    expect((target.element as HTMLDetailsElement).open).toBe(true)
    expect(document.activeElement).toBe(target.find('summary').element)
    expect(wrapper.find('a').exists()).toBe(false)
    wrapper.unmount()
    route.unmount()
  })
  it('renders empty, unavailable, permission, error and limited states honestly', async () => {
    const wrapper = mount(NativeGovernancePanel, { props: { records: true } })
    expect(wrapper.text()).toContain('unavailable')
    expect(wrapper.text()).toContain('No visible native records')
    for (const state of ['permission', 'error', 'limited']) {
      const value = snapshot()
      value.state = state
      value.limited = true
      value.records = []
      await wrapper.setProps({ snapshot: value })
      expect(wrapper.find('[role="status"]').text()).toContain(state)
      expect(wrapper.text()).toContain('Limited scoped snapshot')
      expect(wrapper.text()).not.toContain('review-exact')
    }
  })
  it('renders measured values only when present and hides optional records', () => {
    const value = snapshot()
    value.spend.provider_tokens = 12
    value.spend.provider_cost_usd = 0
    value.spend.unknown_retained = false
    const wrapper = mount(NativeGovernancePanel, { props: { snapshot: value } })
    expect(
      wrapper
        .findAll('dd')
        .slice(0, 2)
        .map((node) => node.text()),
    ).toEqual(['12', '0'])
    expect(wrapper.find('details').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Unknown usage remains reserved')
  })
})
