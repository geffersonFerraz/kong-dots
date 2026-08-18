import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../api/client', () => ({
  api: {
    getState: vi.fn(),
    saveLayout: vi.fn().mockResolvedValue({}),
    pluginSchema: vi.fn(),
    plan: vi.fn(),
    apply: vi.fn(),
    exportDeck: vi.fn(),
    importDeck: vi.fn(),
    history: vi.fn(),
  },
  openSocket: vi.fn(),
}))

import { api } from '../api/client'
import { useGraphStore } from '../stores/graph'
import PropertiesPanel from './PropertiesPanel.vue'

const PLUGIN_SCHEMA = {
  fields: [
    {
      config: {
        type: 'record',
        fields: [
          { minute: { type: 'number', default: null } },
          { policy: { type: 'string', default: 'local', one_of: ['local', 'cluster'] } },
        ],
      },
    },
  ],
}

async function setup(state) {
  setActivePinia(createPinia())
  api.pluginSchema.mockResolvedValue(PLUGIN_SCHEMA)
  api.getState.mockResolvedValue({
    connection: { id: 'conn-1' },
    info: { version: '3.9.1', plugins: ['rate-limiting'] },
    state,
    layout: {},
    fetched_at: '2026-08-18T00:00:00Z',
  })
  const graph = useGraphStore()
  await graph.load('conn-1')
  return graph
}

const EMPTY = { services: [], routes: [], plugins: [], consumers: [], upstreams: [], targets: [] }

function fieldByLabel(wrapper, label) {
  const found = wrapper.findAll('label').find((l) => l.find('span').text().trim().startsWith(label))
  if (!found) throw new Error(`no field labelled "${label}"`)
  return found
}

async function mountPanel(graph) {
  const wrapper = mount(PropertiesPanel)
  await new Promise((r) => setTimeout(r, 0))
  await wrapper.vm.$nextTick()
  return wrapper
}

describe('editing through the properties panel', () => {
  // The end-to-end path the user actually takes: pick a plugin on the canvas,
  // type a config value, and have it reach the payload sent to Kong.
  it('lands a new plugin config in the payload that gets applied', async () => {
    const graph = await setup({ ...EMPTY, services: [{ id: 'svc-1', name: 'api', host: 'a' }] })
    const plugin = graph.createEntity('plugins', { name: 'rate-limiting' })
    const wrapper = await mountPanel(graph)

    await fieldByLabel(wrapper, 'minute').find('input').setValue('17')

    expect(graph.entities.plugins[0].config).toEqual({ minute: 17 })
    const sent = graph.desiredPayload().plugins.find((p) => p.id === plugin.id)
    expect(sent.config).toEqual({ minute: 17 })
    expect(sent.name).toBe('rate-limiting')
  })

  it('edits an existing plugin without losing its other config keys', async () => {
    const graph = await setup({
      ...EMPTY,
      plugins: [
        {
          id: 'plg-1',
          name: 'rate-limiting',
          enabled: true,
          config: { minute: 10, policy: 'local' },
          created_at: 1,
        },
      ],
    })
    graph.selectedNodeId = 'plugins:plg-1'
    const wrapper = await mountPanel(graph)

    await fieldByLabel(wrapper, 'minute').find('input').setValue('42')

    expect(graph.desiredPayload().plugins[0].config).toEqual({ minute: 42, policy: 'local' })
    expect(graph.pending).toMatchObject({ create: 0, update: 1, delete: 0 })
  })

  it('writes core entity fields straight into the canvas state', async () => {
    const graph = await setup({ ...EMPTY, services: [{ id: 'svc-1', name: 'api', host: 'a.internal', port: 80 }] })
    graph.selectedNodeId = 'services:svc-1'
    const wrapper = await mountPanel(graph)

    await fieldByLabel(wrapper, 'Host').find('input').setValue('b.internal')
    await fieldByLabel(wrapper, 'Port').find('input').setValue('8443')

    const svc = graph.desiredPayload().services[0]
    expect(svc.host).toBe('b.internal')
    expect(svc.port).toBe(8443)
  })

  it('turns a Paths textarea into the array Kong expects', async () => {
    const graph = await setup({ ...EMPTY, routes: [{ id: 'rt-1', name: 'r', paths: ['/a'] }] })
    graph.selectedNodeId = 'routes:rt-1'
    const wrapper = await mountPanel(graph)

    await fieldByLabel(wrapper, 'Paths').find('textarea').setValue('/one\n/two')

    expect(graph.desiredPayload().routes[0].paths).toEqual(['/one', '/two'])
  })

  it('shows where a plugin is attached and can detach it', async () => {
    const graph = await setup({
      ...EMPTY,
      services: [{ id: 'svc-1', name: 'api', host: 'a' }],
      plugins: [{ id: 'plg-1', name: 'rate-limiting', config: {}, service: { id: 'svc-1' } }],
    })
    graph.selectedNodeId = 'plugins:plg-1'
    const wrapper = await mountPanel(graph)

    expect(wrapper.text()).toContain('api')
    await wrapper.find('button.text-sky-400').trigger('click')
    expect(graph.entities.plugins[0].service).toBeNull()
  })

  it('confirms before a cascading delete', async () => {
    const graph = await setup({
      ...EMPTY,
      services: [{ id: 'svc-1', name: 'api', host: 'a' }],
      routes: [{ id: 'rt-1', name: 'r', service: { id: 'svc-1' } }],
    })
    graph.selectedNodeId = 'services:svc-1'
    const wrapper = await mountPanel(graph)

    await wrapper.find('footer button').trigger('click')
    expect(graph.entities.services).toHaveLength(1) // nothing removed yet
    expect(wrapper.text()).toMatch(/2 entities will be removed/)

    await wrapper.findAll('button').find((b) => b.text() === 'Remove').trigger('click')
    expect(graph.entities.services).toHaveLength(0)
    expect(graph.entities.routes).toHaveLength(0)
  })

  it('locks the plugin type once Kong has assigned it an id', async () => {
    const graph = await setup({
      ...EMPTY,
      plugins: [{ id: 'plg-1', name: 'rate-limiting', config: {} }],
    })
    graph.selectedNodeId = 'plugins:plg-1'
    const wrapper = await mountPanel(graph)
    expect(fieldByLabel(wrapper, 'Plugin').find('select').attributes('disabled')).toBeDefined()
  })

  // A plugin's name is the plugin type, so it is chosen from what the gateway
  // reports rather than typed — a typo there is a mid-apply failure.
  it('offers the gateway plugin list when the plugin is still a draft', async () => {
    const graph = await setup({ ...EMPTY })
    graph.createEntity('plugins', { name: 'rate-limiting' })
    const wrapper = await mountPanel(graph)

    const select = fieldByLabel(wrapper, 'Plugin').find('select')
    expect(select.attributes('disabled')).toBeUndefined()
    expect(select.findAll('option').map((o) => o.text())).toContain('rate-limiting')
  })
})

describe('validation feedback in the panel', () => {
  it('marks the field Kong would complain about', async () => {
    const graph = await setup({ ...EMPTY })
    const svc = graph.createEntity('services', { name: 'new-service' })
    const wrapper = await mountPanel(graph)

    expect(wrapper.text()).toContain('Kong would reject this')
    expect(wrapper.text()).toMatch(/Host is required/i)

    const host = fieldByLabel(wrapper, 'Host')
    expect(host.find('input').classes().join(' ')).toContain('border-amber-500')

    await host.find('input').setValue('new.internal')
    expect(graph.issues).toEqual([])
    expect(wrapper.text()).not.toContain('Kong would reject this')
  })

  it('leaves a healthy entity unmarked', async () => {
    const graph = await setup({ ...EMPTY, services: [{ id: 'svc-1', name: 'api', host: 'api.internal', port: 80 }] })
    graph.selectedNodeId = 'services:svc-1'
    const wrapper = await mountPanel(graph)

    expect(wrapper.text()).not.toContain('Kong would reject this')
    expect(fieldByLabel(wrapper, 'Host').find('input').classes().join(' ')).not.toContain('border-amber-500')
  })
})
