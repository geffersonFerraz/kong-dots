import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../api/client', () => ({
  api: { getState: vi.fn(), saveLayout: vi.fn().mockResolvedValue({}), pluginSchema: vi.fn() },
  openSocket: vi.fn(),
}))

import { api } from '../api/client'
import { useGraphStore } from '../stores/graph'
import FilterBar from './FilterBar.vue'

const STATE = {
  services: [{ id: 'svc-1', name: 'billing', host: 'billing.internal' }],
  routes: [{ id: 'rt-1', name: 'invoices', paths: ['/invoices'], service: { id: 'svc-1' } }],
  plugins: [],
  consumers: [],
  upstreams: [],
  targets: [],
}

async function mountBar() {
  setActivePinia(createPinia())
  api.getState.mockResolvedValue({
    connection: { id: 'c1' },
    info: { version: '3.9.1', plugins: [] },
    state: JSON.parse(JSON.stringify(STATE)),
    layout: {},
    fetched_at: '2026-08-18T00:00:00Z',
  })
  const graph = useGraphStore()
  await graph.load('c1')
  const wrapper = mount(FilterBar, { attachTo: document.body })
  return { wrapper, graph }
}

const chips = (wrapper) => wrapper.findAll('button[aria-label^="Filter "]')
const resultRows = (wrapper) => wrapper.findAll('ul li button')

describe('FilterBar', () => {
  it('starts as just a search field', async () => {
    const { wrapper } = await mountBar()

    expect(wrapper.find('input[type="text"], input:not([type])').exists()).toBe(true)
    expect(wrapper.find('svg[aria-label="Search the canvas"]').exists()).toBe(true)
    expect(chips(wrapper)).toHaveLength(0)
    expect(wrapper.text()).not.toContain('Hide the rest')
  })

  it('grows the controls once the field is entered', async () => {
    const { wrapper } = await mountBar()
    await wrapper.find('input').trigger('focus')

    expect(chips(wrapper)).toHaveLength(6)
    expect(wrapper.find('input').attributes('placeholder')).toMatch(/name, host, path or uuid/)
  })

  it('shows the counter and results only while filtering', async () => {
    const { wrapper } = await mountBar()
    await wrapper.find('input').trigger('focus')
    expect(wrapper.text()).not.toContain('Hide the rest')

    await wrapper.find('input').setValue('billing')
    expect(wrapper.text()).toContain('1 of 2 nodes')
    expect(wrapper.text()).toContain('Hide the rest')
    expect(resultRows(wrapper)).toHaveLength(1)
    expect(resultRows(wrapper)[0].text()).toContain('billing.internal')
  })

  it('stays open while the kind chips are being toggled', async () => {
    const { wrapper, graph } = await mountBar()
    await wrapper.find('input').trigger('focus')

    await chips(wrapper).find((c) => c.text() === 'Route').trigger('click')
    expect(graph.filter.kinds).toEqual(['routes'])
    expect(chips(wrapper)).toHaveLength(6) // still expanded
  })

  it('collapses on a click outside but keeps the filter running', async () => {
    const { wrapper, graph } = await mountBar()
    await wrapper.find('input').trigger('focus')
    await wrapper.find('input').setValue('/invoices')
    expect(chips(wrapper)).toHaveLength(6)

    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await wrapper.vm.$nextTick()

    expect(chips(wrapper)).toHaveLength(0)
    expect(graph.filter.query).toBe('/invoices')
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['routes:rt-1'])
  })

  it('says how many matched while collapsed, so the dimmed canvas is explained', async () => {
    const { wrapper } = await mountBar()
    await wrapper.find('input').trigger('focus')
    await wrapper.find('input').setValue('billing')
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await wrapper.vm.$nextTick()

    const badge = wrapper.find('span[title$="nodes match"]')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('1')
  })

  it('a click inside does not collapse it', async () => {
    const { wrapper } = await mountBar()
    await wrapper.find('input').trigger('focus')

    wrapper.element.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(chips(wrapper)).toHaveLength(6)
  })

  it('escape clears and puts the dock away', async () => {
    const { wrapper, graph } = await mountBar()
    await wrapper.find('input').trigger('focus')
    await wrapper.find('input').setValue('billing')

    await wrapper.find('input').trigger('keydown.esc')
    expect(graph.filterActive).toBe(false)
    expect(chips(wrapper)).toHaveLength(0)
  })

  it('the clear button is only offered while something is filtered', async () => {
    const { wrapper } = await mountBar()
    expect(wrapper.find('button[title="Clear filter"]').exists()).toBe(false)

    await wrapper.find('input').trigger('focus')
    await wrapper.find('input').setValue('billing')
    expect(wrapper.find('button[title="Clear filter"]').exists()).toBe(true)
  })

  it('picking a result selects the node and closes the dock', async () => {
    const { wrapper, graph } = await mountBar()
    await wrapper.find('input').trigger('focus')
    await wrapper.find('input').setValue('/invoices')

    await resultRows(wrapper)[0].trigger('click')
    expect(graph.selectedNodeId).toBe('routes:rt-1')
    expect(wrapper.emitted('focus-node')[0]).toEqual(['routes:rt-1'])
    expect(chips(wrapper)).toHaveLength(0)
  })

  it('opens from the magnifier icon', async () => {
    const { wrapper } = await mountBar()
    await wrapper.find('svg[aria-label="Search the canvas"]').trigger('click')
    expect(chips(wrapper)).toHaveLength(6)
  })
})
