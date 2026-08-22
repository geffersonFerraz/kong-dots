import { beforeEach, describe, expect, it, vi } from 'vitest'
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
  setIdentity: vi.fn(),
  ApiError: class ApiError extends Error {},
}))

import { api } from '../api/client'
import { useGraphStore } from './graph'
import { useSessionStore } from './session'

const LIVE = {
  services: [{ id: 'svc-1', name: 'billing', host: 'billing.internal', port: 8080, created_at: 1, updated_at: 2 }],
  routes: [{ id: 'rt-1', name: 'invoices', paths: ['/invoices'], service: { id: 'svc-1' }, created_at: 1 }],
  plugins: [
    {
      id: 'plg-1',
      name: 'rate-limiting',
      enabled: true,
      config: { minute: 10, hour: null, policy: 'local', limit_by: 'consumer' },
      route: { id: 'rt-1' },
      created_at: 1,
    },
  ],
  consumers: [{ id: 'con-1', username: 'mobile-app' }],
  upstreams: [{ id: 'up-1', name: 'billing-pool', algorithm: 'round-robin' }],
  targets: [{ id: 'tgt-1', target: '10.0.0.1:8080', weight: 100, upstream: { id: 'up-1' } }],
}

async function freshStore(state = LIVE, layout = {}) {
  setActivePinia(createPinia())
  api.getState.mockResolvedValue({
    connection: { id: 'conn-1' },
    info: { version: '3.9.1', plugins: ['rate-limiting', 'key-auth'] },
    state: JSON.parse(JSON.stringify(state)),
    layout,
    fetched_at: '2026-08-18T00:00:00Z',
  })
  const graph = useGraphStore()
  await graph.load('conn-1')
  return graph
}

const draftsOf = (payload, kind) => payload[kind].filter((e) => String(e.id).startsWith('draft:'))

describe('loading', () => {
  it('separates the live baseline from the editable canvas state', async () => {
    const graph = await freshStore()
    expect(graph.entities.services[0].host).toBe('billing.internal')
    graph.updateEntity('services', 'svc-1', { host: 'changed' })
    expect(graph.baseline.services[0].host).toBe('billing.internal')
    expect(graph.isDirty).toBe(true)
  })

  it('auto-lays out every node and keeps stored positions', async () => {
    const graph = await freshStore(LIVE, { 'services:svc-1': { entity_type: 'services', entity_id: 'svc-1', x: 42, y: 99 } })
    expect(graph.positions['services:svc-1']).toEqual({ x: 42, y: 99 })
    for (const node of graph.nodes) expect(graph.positions[node.id]).toBeDefined()
  })
})

describe('persisting an edit', () => {
  it('keeps a nested plugin config edit in the applied payload', async () => {
    const graph = await freshStore()
    const config = { ...graph.entities.plugins[0].config, minute: 99 }
    graph.updateEntity('plugins', 'plg-1', { config })

    const payload = graph.desiredPayload()
    expect(payload.plugins[0].config.minute).toBe(99)
    // the untouched keys must survive, or Kong would reset them to defaults
    expect(payload.plugins[0].config.policy).toBe('local')
    expect(payload.plugins[0].config.limit_by).toBe('consumer')
  })

  it('sends a newly created plugin with the config the user typed', async () => {
    const graph = await freshStore()
    const plugin = graph.createEntity('plugins', { name: 'rate-limiting' })
    graph.updateEntity('plugins', plugin.id, { config: { minute: 17 } })

    const created = draftsOf(graph.desiredPayload(), 'plugins')
    expect(created).toHaveLength(1)
    expect(created[0].config).toEqual({ minute: 17 })
    expect(created[0].name).toBe('rate-limiting')
    expect(created[0].enabled).toBe(true)
  })

  it('preserves falsy values that are meaningful to Kong', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { enabled: false, retries: 0, path: '' })
    const svc = graph.desiredPayload().services[0]
    expect(svc.enabled).toBe(false)
    expect(svc.retries).toBe(0)
    expect(svc.path).toBe('')
  })

  it('strips only the server-managed fields', async () => {
    const graph = await freshStore()
    const svc = graph.desiredPayload().services[0]
    expect(svc).not.toHaveProperty('created_at')
    expect(svc).not.toHaveProperty('updated_at')
    expect(svc).not.toHaveProperty('ws_id')
    expect(svc.id).toBe('svc-1')
    expect(svc.name).toBe('billing')
  })

  it('carries every kind through, so an untouched kind is never dropped', async () => {
    const graph = await freshStore()
    const payload = graph.desiredPayload()
    expect(Object.keys(payload).sort()).toEqual(
      ['consumers', 'plugins', 'routes', 'services', 'targets', 'upstreams'],
    )
    expect(payload.targets[0].upstream).toEqual({ id: 'up-1' })
  })

  it('reports a local diff that matches what was edited', async () => {
    const graph = await freshStore()
    expect(graph.pending).toMatchObject({ create: 0, update: 0, delete: 0 })

    graph.updateEntity('services', 'svc-1', { host: 'new.internal' })
    graph.createEntity('consumers', { username: 'partner' })
    graph.deleteEntity('targets', 'tgt-1')

    expect(graph.pending).toMatchObject({ create: 1, update: 1, delete: 1 })
    expect(graph.pending.items.map((i) => i.type).sort()).toEqual(['create', 'delete', 'update'])
  })

  it('discards back to the live state', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { host: 'x' })
    graph.createEntity('routes', { name: 'temp' })
    graph.discardChanges()
    expect(graph.isDirty).toBe(false)
    expect(graph.entities.services[0].host).toBe('billing.internal')
  })
})

describe('creating entities', () => {
  it('applies the kind defaults and a draft id', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'new-api' }, { x: 10, y: 20 })
    expect(svc.id).toMatch(/^draft:services-/)
    expect(svc.protocol).toBe('http')
    expect(svc.port).toBe(80)
    expect(graph.selectedNodeId).toBe(`services:${svc.id}`)
    expect(graph.positions[`services:${svc.id}`]).toEqual({ x: 10, y: 20 })
  })

  it('never persists layout rows for entities Kong does not know yet', async () => {
    const graph = await freshStore()
    graph.createEntity('services', { name: 'draft-only' }, { x: 1, y: 2 })
    await graph.persistLayout()
    const [, positions] = api.saveLayout.mock.calls.at(-1)
    expect(positions.every((p) => !p.entity_id.startsWith('draft:'))).toBe(true)
  })
})

describe('connecting nodes', () => {
  it('wires a Route to a Service', async () => {
    const graph = await freshStore()
    const route = graph.createEntity('routes', { name: 'r' })
    expect(graph.connect('services:svc-1', `routes:${route.id}`)).toBeNull()
    expect(graph.entities.routes.find((r) => r.id === route.id).service).toEqual({ id: 'svc-1' })
  })

  it('gives a Plugin exactly one owner', async () => {
    const graph = await freshStore()
    expect(graph.connect('services:svc-1', 'plugins:plg-1')).toBeNull()
    let plugin = graph.entities.plugins[0]
    expect(plugin.service).toEqual({ id: 'svc-1' })
    expect(plugin.route).toBeNull()

    expect(graph.connect('consumers:con-1', 'plugins:plg-1')).toBeNull()
    plugin = graph.entities.plugins[0]
    expect(plugin.consumer).toEqual({ id: 'con-1' })
    expect(plugin.service).toBeNull()
  })

  it('links a Target to an Upstream and a Service to an Upstream by name', async () => {
    const graph = await freshStore()
    const target = graph.createEntity('targets', { target: '10.0.0.2:80' })
    expect(graph.connect('upstreams:up-1', `targets:${target.id}`)).toBeNull()
    expect(graph.entities.targets.find((t) => t.id === target.id).upstream).toEqual({ id: 'up-1' })

    expect(graph.connect('upstreams:up-1', 'services:svc-1')).toBeNull()
    expect(graph.entities.services[0].host).toBe('billing-pool')
  })

  it('rejects relations Kong does not have', async () => {
    const graph = await freshStore()
    expect(graph.connect('routes:rt-1', 'services:svc-1')).toMatch(/not a relation/)
    expect(graph.connect('plugins:plg-1', 'routes:rt-1')).toMatch(/not a relation/)
    expect(graph.connect('services:svc-1', 'services:svc-1')).toMatch(/Invalid/)
  })

  it('clears the foreign key when an edge is removed', async () => {
    const graph = await freshStore()
    graph.disconnect({ target: 'routes:rt-1', data: { relation: 'route-service' } })
    expect(graph.entities.routes[0].service).toBeNull()

    graph.disconnect({ target: 'plugins:plg-1', data: { relation: 'plugin-route' } })
    expect(graph.entities.plugins[0].route).toBeNull()
  })

  it('derives one edge per real relation', async () => {
    const graph = await freshStore()
    const relations = graph.edges.map((e) => e.data.relation).sort()
    expect(relations).toEqual(['plugin-route', 'route-service', 'target-upstream'])
  })
})

describe('deleting', () => {
  it('cascades a Service through its Routes and their Plugins', async () => {
    const graph = await freshStore()
    const victims = graph.cascade('services', 'svc-1')
    expect(victims.map((v) => v.kind).sort()).toEqual(['plugins', 'routes', 'services'])

    graph.deleteEntity('services', 'svc-1')
    expect(graph.entities.services).toHaveLength(0)
    expect(graph.entities.routes).toHaveLength(0)
    expect(graph.entities.plugins).toHaveLength(0)
    expect(graph.positions['services:svc-1']).toBeUndefined()
  })

  it('cascades an Upstream through its Targets but leaves Services alone', async () => {
    const graph = await freshStore()
    graph.deleteEntity('upstreams', 'up-1')
    expect(graph.entities.targets).toHaveLength(0)
    expect(graph.entities.services).toHaveLength(1)
  })
})

describe('applying', () => {
  it('moves canvas positions from draft ids to the ids Kong assigned', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'brand-new' }, { x: 500, y: 250 })
    api.apply.mockResolvedValue({
      plan: { ops: [{ type: 'create', kind: 'services', entity_id: svc.id }] },
      result: { status: 'success', results: [], id_map: { [svc.id]: 'real-svc-9' } },
    })
    await graph.apply()

    expect(graph.positions['services:real-svc-9']).toEqual({ x: 500, y: 250 })
    expect(graph.positions[`services:${svc.id}`]).toBeUndefined()
    const [, positions] = api.saveLayout.mock.calls.at(-1)
    expect(positions).toContainEqual({ entity_type: 'services', entity_id: 'real-svc-9', x: 500, y: 250 })
  })

  it('reports a failed apply instead of pretending it worked', async () => {
    const graph = await freshStore()
    graph.createEntity('services', { name: 'boom' })
    api.apply.mockResolvedValue({
      plan: { ops: [{ type: 'create', kind: 'services', entity_id: 'draft:x' }] },
      result: { status: 'partial', error: 'schema violation', results: [], id_map: {} },
    })
    await graph.apply()
    expect(graph.toast.kind).toBe('error')
    expect(graph.toast.message).toMatch(/schema violation/)
  })
})

describe('decK import', () => {
  it('replaces the canvas and lays the result out', async () => {
    const graph = await freshStore()
    api.importDeck.mockResolvedValue({
      state: {
        services: [{ id: 'draft:service-1', name: 'imported' }],
        routes: [],
        plugins: [],
        consumers: [],
        upstreams: [],
        targets: [],
      },
    })
    await graph.importDeck('_format_version: "3.0"')
    expect(graph.entities.services).toHaveLength(1)
    expect(graph.entities.services[0].name).toBe('imported')
    expect(graph.positions['services:draft:service-1']).toBeDefined()
  })
})

describe('canvas filter', () => {
  it('matches on name and on uuid, whole or partial', async () => {
    const graph = await freshStore()

    graph.setFilter({ query: 'billing' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['services:svc-1', 'upstreams:up-1'])

    graph.setFilter({ query: 'PLG-1' }) // case-insensitive uuid
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['plugins:plg-1'])

    graph.setFilter({ query: 'rt-' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['routes:rt-1'])

    graph.setFilter({ query: 'nothing-matches-this' })
    expect(graph.filterResults).toEqual([])
  })

  it('matches a Service by its host', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'billing.internal' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['services:svc-1'])

    // a partial host is enough, and it does not drag in same-named entities
    graph.setFilter({ query: '.internal' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['services:svc-1'])
  })

  it('matches a Route by its path', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: '/invoices' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['routes:rt-1'])

    graph.setFilter({ query: 'invoic' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['routes:rt-1'])
  })

  it('matches any entry of a list field, and Route hosts and methods too', async () => {
    const graph = await freshStore({
      ...LIVE,
      routes: [
        {
          id: 'rt-1',
          name: 'multi',
          paths: ['/a', '/deep/second'],
          hosts: ['api.example.com'],
          methods: ['GET', 'PATCH'],
          service: { id: 'svc-1' },
        },
      ],
    })
    for (const query of ['/deep/second', 'api.example.com', 'patch']) {
      graph.setFilter({ query })
      expect(graph.filterResults.map((r) => r.nodeId), `query ${query}`).toEqual(['routes:rt-1'])
    }
  })

  it('does not search fields that belong to another kind', async () => {
    // "billing.internal" is a Service host; no Route should answer to it.
    const graph = await freshStore()
    graph.setFilter({ query: 'billing.internal', kinds: ['routes'] })
    expect(graph.filterResults).toEqual([])
  })

  it('shows what matched next to each result', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'billing.internal' })
    expect(graph.filterResults[0].detail).toContain('billing.internal')

    graph.setFilter({ query: '/invoices' })
    expect(graph.filterResults[0].detail).toContain('/invoices')
  })

  it('matches consumers by username and targets by address', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'mobile' })
    expect(graph.filterResults.map((r) => r.label)).toEqual(['mobile-app'])

    graph.setFilter({ query: '10.0.0.1:8080' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['targets:tgt-1'])
  })

  it('narrows by kind, with or without a query', async () => {
    const graph = await freshStore()

    graph.setFilter({ kinds: ['plugins'] })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['plugins:plg-1'])

    graph.setFilter({ kinds: ['services', 'routes'], query: 'billing' })
    expect(graph.filterResults.map((r) => r.nodeId)).toEqual(['services:svc-1'])
  })

  it('dims what does not match instead of losing it', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'billing' })

    expect(graph.nodes).toHaveLength(graph.allNodes.length)
    const dimmed = graph.nodes.filter((n) => n.data.dimmed).map((n) => n.id)
    expect(dimmed).toContain('routes:rt-1')
    expect(dimmed).not.toContain('services:svc-1')
  })

  it('hides the rest on request, without dangling edges', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'billing', hideUnmatched: true })

    expect(graph.nodes.map((n) => n.id).sort()).toEqual(['services:svc-1', 'upstreams:up-1'])
    for (const edge of graph.edges) {
      expect(graph.nodes.some((n) => n.id === edge.source)).toBe(true)
      expect(graph.nodes.some((n) => n.id === edge.target)).toBe(true)
    }
  })

  it('fades edges that only half-match', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'invoices' }) // the route only
    const routeEdge = graph.edges.find((e) => e.data.relation === 'route-service')
    expect(routeEdge.style.opacity).toBe(0.15)
  })

  it('is a view concern only — it never changes what gets applied', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'billing', hideUnmatched: true })

    const payload = graph.desiredPayload()
    expect(payload.routes).toHaveLength(1)
    expect(payload.plugins).toHaveLength(1)
    expect(payload.consumers).toHaveLength(1)
    expect(graph.pending).toMatchObject({ create: 0, update: 0, delete: 0 })
  })

  it('lays out hidden nodes too, so unfiltering does not pile them up', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'billing', hideUnmatched: true })
    graph.positions = {}
    graph.applyAutoLayout()
    for (const node of graph.allNodes) expect(graph.positions[node.id]).toBeDefined()
  })

  it('clears back to showing everything', async () => {
    const graph = await freshStore()
    graph.setFilter({ query: 'x', kinds: ['plugins'], hideUnmatched: true })
    expect(graph.filterActive).toBe(true)
    graph.clearFilter()
    expect(graph.filterActive).toBe(false)
    expect(graph.matchedNodeIds).toBeNull()
    expect(graph.nodes).toHaveLength(graph.allNodes.length)
    expect(graph.nodes.every((n) => !n.data.dimmed)).toBe(true)
  })
})

describe('validation before applying', () => {
  it('flags a Service created without a host — the case Kong rejects', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'new-service' })

    expect(graph.issues).toHaveLength(1)
    expect(graph.issues[0]).toMatchObject({
      nodeId: `services:${svc.id}`,
      kind: 'services',
      field: 'host',
      label: 'new-service',
    })
    expect(graph.issues[0].message).toMatch(/host is required/i)

    graph.updateEntity('services', svc.id, { host: 'new.internal' })
    expect(graph.issues).toEqual([])
  })

  it('requires a Route to carry at least one matcher', async () => {
    const graph = await freshStore()
    const route = graph.createEntity('routes', { name: 'no-match', paths: [] })
    expect(graph.issues.map((i) => i.message)).toEqual([
      expect.stringMatching(/at least one matcher/i),
    ])

    graph.updateEntity('routes', route.id, { methods: ['GET'] })
    expect(graph.issues).toEqual([])
  })

  it('accepts a Consumer identified by either username or custom id', async () => {
    const graph = await freshStore()
    const consumer = graph.createEntity('consumers', {})
    expect(graph.issues.map((i) => i.message)).toEqual([expect.stringMatching(/username or a custom ID/i)])

    graph.updateEntity('consumers', consumer.id, { custom_id: 'partner-7' })
    expect(graph.issues).toEqual([])
  })

  it('requires a Target to be attached to an Upstream', async () => {
    const graph = await freshStore()
    const target = graph.createEntity('targets', { target: '10.0.0.5:8080' })
    expect(graph.issues.map((i) => i.field)).toEqual(['upstream'])

    graph.connect('upstreams:up-1', `targets:${target.id}`)
    expect(graph.issues).toEqual([])
  })

  it('flags a Plugin with no name and a Service with an impossible port', async () => {
    const graph = await freshStore()
    graph.createEntity('plugins', { name: '' })
    graph.createEntity('services', { name: 'bad-port', host: 'x', port: 70000 })
    expect(graph.issues.map((i) => i.field).sort()).toEqual(['name', 'port'])
  })

  it('only validates what would actually be sent', async () => {
    // A Service already living in Kong is by definition acceptable to Kong,
    // even if it would not pass the canvas rules.
    const graph = await freshStore({
      ...LIVE,
      services: [{ id: 'svc-1', name: 'nameless-host', host: '', created_at: 1 }],
    })
    expect(graph.issues).toEqual([])

    graph.updateEntity('services', 'svc-1', { name: 'touched' })
    expect(graph.issues.map((i) => i.field)).toEqual(['host'])
  })

  it('exposes issues per node so the canvas can mark them', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'incomplete' })
    const node = graph.nodes.find((n) => n.id === `services:${svc.id}`)
    expect(node.data.issues).toHaveLength(1)

    const healthy = graph.nodes.find((n) => n.id === 'services:svc-1')
    expect(healthy.data.issues).toBeUndefined()
  })
})

describe('a failed apply keeps the work on the canvas', () => {
  const failingApply = (graph, { ops, results, idMap = {}, status = 'partial', error = 'boom' }) => {
    api.apply.mockResolvedValue({ plan: { ops }, result: { status, error, results, id_map: idMap } })
  }

  it('keeps the drafts Kong rejected', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'new-service', host: 'x' })
    const route = graph.createEntity('routes', { name: 'new-route', paths: ['/new'] })
    graph.connect(`services:${svc.id}`, `routes:${route.id}`)

    failingApply(graph, {
      ops: [
        { type: 'create', kind: 'services', entity_id: svc.id, label: 'service new-service' },
        { type: 'create', kind: 'routes', entity_id: route.id, label: 'route new-route' },
      ],
      results: [
        { status: 'error', error: 'schema violation (host: required field missing)' },
        { status: 'skipped' },
      ],
      status: 'failed',
      error: 'create service new-service: schema violation',
    })
    await graph.apply()

    // Both are still there, still drafts, still wired together.
    expect(graph.entities.services.map((s) => s.name)).toContain('new-service')
    expect(graph.entities.routes.map((r) => r.name)).toContain('new-route')
    expect(graph.pending).toMatchObject({ create: 2 })
    const keptRoute = graph.entities.routes.find((r) => r.name === 'new-route')
    expect(keptRoute.service).toEqual({ id: svc.id })
    expect(graph.toast.kind).toBe('error')
  })

  it('adopts what did succeed and keeps only the rest pending', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'new-service', host: 'ok.internal' })
    const route = graph.createEntity('routes', { name: 'new-route', paths: ['/new'] })
    graph.connect(`services:${svc.id}`, `routes:${route.id}`)

    // Kong accepted the service and refused the route; the reload therefore
    // reports the service as live.
    api.getState.mockResolvedValue({
      connection: { id: 'conn-1' },
      info: { version: '3.9.1', plugins: [] },
      state: {
        ...JSON.parse(JSON.stringify(LIVE)),
        services: [
          ...JSON.parse(JSON.stringify(LIVE.services)),
          { id: 'real-svc', name: 'new-service', host: 'ok.internal', port: 80, created_at: 5 },
        ],
      },
      layout: {},
      fetched_at: '2026-08-18T00:00:00Z',
    })
    failingApply(graph, {
      ops: [
        { type: 'create', kind: 'services', entity_id: svc.id },
        { type: 'create', kind: 'routes', entity_id: route.id },
      ],
      results: [{ status: 'ok', new_id: 'real-svc' }, { status: 'error', error: 'no matcher' }],
      idMap: { [svc.id]: 'real-svc' },
    })
    await graph.apply()

    // The service is now Kong's copy — no phantom diff for it.
    const created = graph.entities.services.find((s) => s.name === 'new-service')
    expect(created.id).toBe('real-svc')
    expect(created.created_at).toBe(5)

    // The route is still a draft, and now points at the real service id.
    const pendingRoute = graph.entities.routes.find((r) => r.name === 'new-route')
    expect(pendingRoute.id).toMatch(/^draft:/)
    expect(pendingRoute.service).toEqual({ id: 'real-svc' })

    expect(graph.pending).toMatchObject({ create: 1, update: 0, delete: 0 })
  })

  it('keeps a delete pending when Kong refused it', async () => {
    const graph = await freshStore()
    graph.deleteEntity('consumers', 'con-1')

    failingApply(graph, {
      ops: [{ type: 'delete', kind: 'consumers', entity_id: 'con-1' }],
      results: [{ status: 'error', error: 'foreign key violation' }],
      status: 'failed',
    })
    await graph.apply()

    expect(graph.entities.consumers).toHaveLength(0) // still removed on the canvas
    expect(graph.pending).toMatchObject({ delete: 1 }) // so the delete stays pending
  })

  it('re-plans after a failure so the review panel shows what is left', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'clash', host: 'x' })
    api.plan.mockResolvedValue({
      ops: [{ type: 'create', kind: 'services', entity_id: svc.id, label: 'service clash' }],
      summary: { create: 1, update: 0, delete: 0 },
    })
    failingApply(graph, {
      ops: [{ type: 'create', kind: 'services', entity_id: svc.id }],
      results: [{ status: 'error', error: 'unique constraint violation' }],
      status: 'failed',
    })
    await graph.apply()

    expect(graph.plan).not.toBeNull()
    expect(graph.plan.ops).toHaveLength(1)
    expect(api.plan).toHaveBeenCalled()
  })

  it('changes nothing when the apply request itself fails', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'offline', host: 'x' })
    api.apply.mockRejectedValue(new Error('Failed to fetch'))

    const before = JSON.stringify(graph.entities)
    await graph.apply()

    expect(JSON.stringify(graph.entities)).toBe(before)
    expect(graph.entities.services.some((s) => s.id === svc.id)).toBe(true)
    expect(graph.toast.message).toMatch(/Failed to fetch/)
  })

  it('still does a clean reload when everything worked', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'clean', host: 'x' })
    api.apply.mockResolvedValue({
      plan: { ops: [{ type: 'create', kind: 'services', entity_id: svc.id }] },
      result: { status: 'success', results: [{ status: 'ok', new_id: 'real' }], id_map: { [svc.id]: 'real' } },
    })
    await graph.apply()

    // The reload returns the original LIVE fixture, so the draft is gone.
    expect(graph.entities.services.map((s) => s.id)).toEqual(['svc-1'])
    expect(graph.isDirty).toBe(false)
    expect(graph.toast.kind).toBe('success')
  })
})

describe("Kong's uniqueness constraints, caught before the 409", () => {
  // Duplicating a plugin keeps its type and its scope, which is exactly the
  // pair Kong refuses. The canvas has to say so instead of failing mid-apply.
  it('flags a second plugin of the same type on the same scope', async () => {
    const graph = await freshStore()
    const copy = graph.createEntity('plugins', {
      name: 'rate-limiting',
      config: { minute: 10 },
      route: { id: 'rt-1' },
    })

    expect(graph.issues).toHaveLength(1)
    expect(graph.issues[0]).toMatchObject({ nodeId: `plugins:${copy.id}`, field: 'name' })
    expect(graph.issues[0].message).toMatch(/Route invoices already has a rate-limiting plugin/i)

    // Moving it to another scope resolves it, which is what the user meant to do.
    graph.connect('services:svc-1', `plugins:${copy.id}`)
    expect(graph.issues).toEqual([])
  })

  it('allows the same plugin type on different scopes, and flags two globals', async () => {
    const graph = await freshStore()
    graph.createEntity('plugins', { name: 'key-auth', service: { id: 'svc-1' } })
    graph.createEntity('plugins', { name: 'key-auth', route: { id: 'rt-1' } })
    expect(graph.issues).toEqual([])

    graph.createEntity('plugins', { name: 'correlation-id' })
    graph.createEntity('plugins', { name: 'correlation-id' })
    expect(graph.issues.map((i) => i.message)).toEqual([
      expect.stringMatching(/This gateway already has a correlation-id plugin/i),
      expect.stringMatching(/This gateway already has a correlation-id plugin/i),
    ])
  })

  it('flags a name that another Service already uses', async () => {
    const graph = await freshStore()
    graph.createEntity('services', { name: 'billing', host: 'other.internal' })

    expect(graph.issues).toHaveLength(1)
    expect(graph.issues[0].field).toBe('name')
    expect(graph.issues[0].message).toMatch(/an existing Service already uses the name "billing"/i)
  })

  it('names the clash as "another new" when both sides are drafts', async () => {
    const graph = await freshStore()
    graph.createEntity('routes', { name: 'twin', paths: ['/a'] })
    graph.createEntity('routes', { name: 'twin', paths: ['/b'] })
    expect(graph.issues.map((i) => i.message)).toEqual([
      expect.stringMatching(/another new Route already uses the name "twin"/i),
      expect.stringMatching(/another new Route already uses the name "twin"/i),
    ])
  })

  it('checks a Consumer on both username and custom id', async () => {
    const graph = await freshStore()
    const dup = graph.createEntity('consumers', { username: 'mobile-app' })
    expect(graph.issues.map((i) => i.field)).toEqual(['username'])

    graph.updateEntity('consumers', dup.id, { username: 'other', custom_id: 'app-ios' })
    expect(graph.issues).toEqual([])

    graph.updateEntity('consumers', dup.id, { custom_id: 'shared' })
    graph.createEntity('consumers', { username: 'third', custom_id: 'shared' })
    expect(graph.issues.map((i) => i.field)).toEqual(['custom_id', 'custom_id'])
  })

  it('does not flag an untouched entity against itself', async () => {
    const graph = await freshStore()
    expect(graph.issues).toEqual([])

    // editing something unrelated must not make it collide with its own name
    graph.updateEntity('services', 'svc-1', { host: 'moved.internal' })
    expect(graph.issues).toEqual([])
  })

  it('does not flag against an entity that is being deleted', async () => {
    const graph = await freshStore()
    graph.deleteEntity('services', 'svc-1')
    graph.createEntity('services', { name: 'billing', host: 'new.internal' })
    expect(graph.issues).toEqual([])
  })
})

describe('clipboard — moving work between Kongs', () => {
  it('bundles a Service with its Routes and every plugin on them', async () => {
    const graph = await freshStore()
    const bundle = graph.clipboardBundle('services', 'svc-1')

    expect(bundle.kong_flow).toBe(1)
    expect(bundle.kind).toBe('services')
    expect(bundle.label).toBe('billing')
    expect(bundle.entities.services).toHaveLength(1)
    expect(bundle.entities.routes).toHaveLength(1)
    expect(bundle.entities.plugins).toHaveLength(1)
    expect(bundle.entities.consumers).toHaveLength(0)
  })

  it('strips the ids of the source Kong, keeping the wiring between members', async () => {
    const graph = await freshStore()
    const bundle = graph.clipboardBundle('services', 'svc-1')

    const [svc] = bundle.entities.services
    const [route] = bundle.entities.routes
    const [plugin] = bundle.entities.plugins

    for (const e of [svc, route, plugin]) {
      expect(e.id).toMatch(/^draft:paste-/)
      expect(e).not.toHaveProperty('created_at')
      expect(e).not.toHaveProperty('updated_at')
    }
    // internal references survive, pointing at the placeholders
    expect(route.service).toEqual({ id: svc.id })
    expect(plugin.route).toEqual({ id: route.id })
    expect(JSON.stringify(bundle)).not.toContain('svc-1')
  })

  it('drops references that point outside the bundle', async () => {
    const graph = await freshStore()
    // a Route on its own: its Service belongs to the Kong being left behind
    const bundle = graph.clipboardBundle('routes', 'rt-1')

    expect(bundle.entities.routes).toHaveLength(1)
    expect(bundle.entities.plugins).toHaveLength(1)
    expect(bundle.entities.routes[0].service).toBeNull()
    expect(bundle.entities.plugins[0].route).toEqual({ id: bundle.entities.routes[0].id })
  })

  it('pastes into an empty Kong as drafts, still wired together', async () => {
    const source = await freshStore()
    const bundle = source.clipboardBundle('services', 'svc-1')

    // a different workspace, with nothing in it
    const target = await freshStore({ services: [], routes: [], plugins: [], consumers: [], upstreams: [], targets: [] })
    const created = target.pasteBundle(bundle, { x: 100, y: 100 })

    expect(created).toMatchObject({ services: 1, routes: 1, plugins: 1 })
    const svc = target.entities.services[0]
    const route = target.entities.routes[0]
    const plugin = target.entities.plugins[0]

    expect(svc.name).toBe('billing')
    expect(svc.host).toBe('billing.internal')
    expect(route.service).toEqual({ id: svc.id })
    expect(plugin.route).toEqual({ id: route.id })
    expect(plugin.config.minute).toBe(10)

    // everything is pending creation, and nothing carries an id from the source
    expect(target.pending).toMatchObject({ create: 3, update: 0, delete: 0 })
    for (const e of [svc, route, plugin]) expect(e.id).toMatch(/^draft:/)
  })

  it('can be pasted twice, each copy taking a free name', async () => {
    const source = await freshStore()
    const bundle = source.clipboardBundle('routes', 'rt-1')
    const target = await freshStore({ services: [], routes: [], plugins: [], consumers: [], upstreams: [], targets: [] })

    target.pasteBundle(bundle, { x: 0, y: 0 })
    target.pasteBundle(bundle, { x: 400, y: 0 })

    const ids = target.entities.routes.map((r) => r.id)
    expect(ids).toHaveLength(2)
    expect(new Set(ids).size).toBe(2)
    // Kong keeps route names unique, so the second copy cannot reuse the first's.
    expect(target.entities.routes.map((r) => r.name)).toEqual(['invoices', 'invoices-copy'])
    expect(target.issues).toEqual([])
  })

  it('pastes back into the Kong it came from without colliding', async () => {
    const graph = await freshStore()
    const bundle = graph.clipboardBundle('services', 'svc-1')

    graph.pasteBundle(bundle, { x: 0, y: 0 })

    expect(graph.entities.services.map((s) => s.name)).toEqual(['billing', 'billing-copy'])
    expect(graph.entities.routes.map((r) => r.name)).toEqual(['invoices', 'invoices-copy'])
    // The rename is what keeps Kong from answering the apply with a 409.
    expect(graph.issues).toEqual([])
  })

  it('places what it pasted on the canvas and selects it', async () => {
    const source = await freshStore()
    const bundle = source.clipboardBundle('services', 'svc-1')
    const target = await freshStore({ services: [], routes: [], plugins: [], consumers: [], upstreams: [], targets: [] })

    target.pasteBundle(bundle, { x: 500, y: 300 })
    const svc = target.entities.services[0]
    expect(target.positions[`services:${svc.id}`]).toEqual({ x: 500, y: 300 })
    expect(target.selectedNodeId).toBe(`services:${svc.id}`)
  })


  it('carries the Upstream a Service points at, and its Targets', async () => {
    // A Service whose host is an Upstream name: copied on its own, it would
    // land in the other Kong pointing at a balancer that is not there.
    const graph = await freshStore({
      ...LIVE,
      services: [{ id: 'svc-1', name: 'billing', host: 'billing-pool', port: 8080 }],
    })
    const bundle = graph.clipboardBundle('services', 'svc-1')

    expect(bundle.entities.upstreams).toHaveLength(1)
    expect(bundle.entities.upstreams[0].name).toBe('billing-pool')
    expect(bundle.entities.targets).toHaveLength(1)
    expect(bundle.entities.targets[0].target).toBe('10.0.0.1:8080')
    expect(bundle.entities.targets[0].upstream).toEqual({ id: bundle.entities.upstreams[0].id })

    const target = await freshStore({ services: [], routes: [], plugins: [], consumers: [], upstreams: [], targets: [] })
    target.pasteBundle(bundle, { x: 0, y: 0 })
    expect(target.entities.services[0].host).toBe('billing-pool')
    expect(target.entities.targets[0].upstream).toEqual({ id: target.entities.upstreams[0].id })
  })

  it('keeps a renamed Upstream wired to the Service that named it', async () => {
    const graph = await freshStore({
      ...LIVE,
      services: [{ id: 'svc-1', name: 'billing', host: 'billing-pool', port: 8080 }],
    })
    const bundle = graph.clipboardBundle('services', 'svc-1')
    graph.pasteBundle(bundle, { x: 0, y: 0 })

    const copy = graph.entities.services.find((s) => s.name === 'billing-copy')
    expect(graph.entities.upstreams.map((u) => u.name)).toEqual(['billing-pool', 'billing-pool-copy'])
    // The copy must follow its own Upstream, not the original one.
    expect(copy.host).toBe('billing-pool-copy')
    expect(graph.issues).toEqual([])
  })

  it('leaves the Upstream alone when a Service merely resolves a hostname', async () => {
    const graph = await freshStore() // host is billing.internal, not an Upstream
    const bundle = graph.clipboardBundle('services', 'svc-1')
    expect(bundle.entities.upstreams).toHaveLength(0)
    expect(bundle.entities.targets).toHaveLength(0)
  })

  it('carries an Upstream copied on its own with its Targets', async () => {
    const graph = await freshStore()
    const bundle = graph.clipboardBundle('upstreams', 'up-1')
    expect(bundle.entities.upstreams).toHaveLength(1)
    expect(bundle.entities.targets).toHaveLength(1)
  })

  it('carries the plugins attached to a Consumer', async () => {
    const graph = await freshStore({
      ...LIVE,
      plugins: [{ id: 'plg-2', name: 'key-auth', enabled: true, config: {}, consumer: { id: 'con-1' } }],
    })
    const bundle = graph.clipboardBundle('consumers', 'con-1')
    expect(bundle.entities.consumers).toHaveLength(1)
    expect(bundle.entities.plugins).toHaveLength(1)
    expect(bundle.entities.plugins[0].consumer).toEqual({ id: bundle.entities.consumers[0].id })
  })

  it('refuses anything that is not one of its own bundles', async () => {
    const graph = await freshStore()
    for (const junk of [null, {}, { kong_flow: 2, entities: {} }, { kong_flow: 1 }, 'nope']) {
      expect(() => graph.pasteBundle(junk)).toThrow(/does not look like/)
    }
  })
})

describe('working alongside other people', () => {
  it('sends the baseline with every plan, so the backend can tell a delete from somebody else\'s create', async () => {
    const graph = await freshStore()
    graph.deleteEntity('consumers', 'con-1')
    api.plan.mockResolvedValue({ ops: [], summary: { create: 0, update: 0, delete: 1 } })

    await graph.buildPlan()

    const [, body] = api.plan.mock.calls.at(-1)
    expect(body.desired.consumers).toHaveLength(0)
    // The baseline still holds what Kong reported when the canvas loaded...
    expect(body.baseline.consumers.map((c) => c.id)).toEqual(['con-1'])
    // ...stripped of the fields Kong reports but never accepts back.
    expect(body.baseline.services[0]).not.toHaveProperty('created_at')
    expect(body.actor).toBeTruthy()
    expect(body.client_id).toBeTruthy()
  })

  it('keeps the canvas and shows the drift when Kong moved underneath it', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { host: 'mine.internal' })

    const conflict = {
      kind: 'services',
      entity_id: 'svc-1',
      label: 'service billing',
      op: 'update',
      reason: 'changed',
      changes: [{ field: 'host', from: 'billing.internal', to: 'theirs.internal' }],
    }
    const refused = Object.assign(new Error('1 entity changed in Kong since this canvas loaded'), {
      status: 409,
      body: { plan: { ops: [], summary: { create: 0, update: 1, delete: 0 }, conflicts: [conflict] } },
    })
    api.apply.mockRejectedValue(refused)

    const res = await graph.apply()

    expect(res).toBeNull()
    expect(graph.conflicts).toEqual([conflict])
    // Nothing was applied, so the user's own edit is still on the canvas.
    expect(graph.entities.services[0].host).toBe('mine.internal')
    expect(graph.toast.kind).toBe('error')
  })

  it('reports what the plan left alone because this canvas never saw it', async () => {
    const graph = await freshStore()
    api.plan.mockResolvedValue({
      ops: [],
      summary: { create: 0, update: 0, delete: 0 },
      ignored: [{ kind: 'services', entity_id: 's-new', label: 'service legacy' }],
    })
    await graph.buildPlan()
    expect(graph.ignored.map((i) => i.label)).toEqual(['service legacy'])
  })

  it('files the change instead of applying it when this browser may not push to Kong', async () => {
    const graph = await freshStore()
    graph.createEntity('services', { name: 'proposed', host: 'x' })
    api.apply.mockResolvedValue({
      status: 'pending_approval',
      request: { id: 'req-1', status: 'pending', requested_by: 'bob' },
    })

    await graph.apply({ title: 'add the proposed service' })

    expect(graph.filedRequest).toMatchObject({ id: 'req-1', status: 'pending' })
    // The work stays on the canvas: it has not reached Kong, so it is still the
    // user's to keep editing.
    expect(graph.entities.services.some((s) => s.name === 'proposed')).toBe(true)
    expect(graph.isDirty).toBe(true)
    expect(graph.toast.message).toMatch(/approval/i)
  })

  it('flags somebody else\'s apply, and ignores the echo of its own', async () => {
    const graph = await freshStore()
    const session = useSessionStore()

    graph.noteRemoteChange({ by: session.clientId, actor: 'me' })
    expect(graph.remoteChange).toBeNull()

    graph.noteRemoteChange({ by: 'another-tab', actor: 'alice', summary: { create: 1 } })
    expect(graph.remoteChange).toMatchObject({ actor: 'alice' })

    graph.dismissRemoteChange()
    expect(graph.remoteChange).toBeNull()
  })
})

describe('a node dragged by somebody else', () => {
  it('moves on this canvas without counting as a change to apply', async () => {
    const graph = await freshStore()
    const before = graph.pending

    graph.applyRemotePosition({ id: 'tab-b', name: 'bob', node: 'services:svc-1', x: 640, y: 220 })

    expect(graph.positions['services:svc-1']).toEqual({ x: 640, y: 220 })
    // Layout is not part of the Kong state, so following somebody's drag must
    // never make this canvas look dirty.
    expect(graph.pending).toEqual(before)
    expect(graph.isDirty).toBe(false)
  })

  it('is ignored for a node this browser is dragging right now', async () => {
    const graph = await freshStore()
    graph.setPosition('services:svc-1', { x: 100, y: 100 })
    graph.beginLocalDrag(['services:svc-1'])

    graph.applyRemotePosition({ node: 'services:svc-1', x: 900, y: 900 })
    expect(graph.positions['services:svc-1']).toEqual({ x: 100, y: 100 })

    // Once the drag is over, their moves land again.
    graph.endLocalDrag()
    graph.applyRemotePosition({ node: 'services:svc-1', x: 900, y: 900 })
    expect(graph.positions['services:svc-1']).toEqual({ x: 900, y: 900 })
  })

  it('still follows the other nodes while one is being dragged here', async () => {
    const graph = await freshStore()
    graph.beginLocalDrag(['services:svc-1'])

    graph.applyRemotePosition({ node: 'routes:rt-1', x: 400, y: 50 })
    expect(graph.positions['routes:rt-1']).toEqual({ x: 400, y: 50 })
  })

  it('ignores a frame that names no node', async () => {
    const graph = await freshStore()
    const before = JSON.stringify(graph.positions)
    graph.applyRemotePosition({ x: 10, y: 10 })
    graph.applyRemotePosition(null)
    expect(JSON.stringify(graph.positions)).toBe(before)
  })
})

// A session with somebody else present, so edits are actually published.
function withPeer() {
  const session = useSessionStore()
  const sent = []
  session.attach({ push: (type, payload) => sent.push({ type, payload }) })
  session.setPeers([{ id: session.clientId, name: 'me' }, { id: 'tab-b', name: 'bob' }])
  return { session, sent, ops: () => sent.filter((m) => m.type === 'canvas_op') }
}

describe('undo and redo', () => {
  it('takes back a created node, and puts it back on redo', async () => {
    const graph = await freshStore()
    const svc = graph.createEntity('services', { name: 'new', host: 'x' })
    expect(graph.entities.services.some((s) => s.id === svc.id)).toBe(true)

    expect(graph.undo().label).toMatch(/Add Service/)
    expect(graph.entities.services.some((s) => s.id === svc.id)).toBe(false)
    expect(graph.canRedo).toBe(true)

    graph.redo()
    expect(graph.entities.services.some((s) => s.id === svc.id)).toBe(true)
    expect(graph.canUndo).toBe(true)
  })

  it('restores the previous value of an edited field', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { host: 'changed.internal' })
    expect(graph.entities.services[0].host).toBe('changed.internal')

    graph.undo()
    expect(graph.entities.services[0].host).toBe('billing.internal')
    expect(graph.isDirty).toBe(false)

    graph.redo()
    expect(graph.entities.services[0].host).toBe('changed.internal')
  })

  it('brings back everything a cascading delete removed, in one step', async () => {
    const graph = await freshStore()
    // Deleting the Service takes its Route and that Route's plugin with it.
    graph.deleteEntity('services', 'svc-1')
    expect(graph.entities.services).toHaveLength(0)
    expect(graph.entities.routes).toHaveLength(0)
    expect(graph.entities.plugins).toHaveLength(0)

    graph.undo()
    expect(graph.entities.services).toHaveLength(1)
    expect(graph.entities.routes).toHaveLength(1)
    expect(graph.entities.plugins).toHaveLength(1)
    // The wiring comes back too, not just the rows.
    expect(graph.entities.routes[0].service).toEqual({ id: 'svc-1' })
    expect(graph.isDirty).toBe(false)
  })

  it('counts a connection as one action even though it edits through another', async () => {
    const graph = await freshStore()
    const plugin = graph.createEntity('plugins', { name: 'key-auth' })
    const depth = graph.undoStack.length

    graph.connect('services:svc-1', `plugins:${plugin.id}`)
    expect(graph.undoStack.length).toBe(depth + 1)
    expect(graph.entities.plugins.find((p) => p.id === plugin.id).service).toEqual({ id: 'svc-1' })

    graph.undo()
    // The plugin goes back to exactly what it was: created without a `service`
    // key at all, rather than with one set to null.
    const restored = graph.entities.plugins.find((p) => p.id === plugin.id)
    expect(restored.service ?? null).toBeNull()
    expect(graph.allEdges.some((e) => e.target === `plugins:${plugin.id}`)).toBe(false)
  })

  it('undoes a paste as a single step', async () => {
    const graph = await freshStore()
    const bundle = graph.clipboardBundle('services', 'svc-1')
    const before = graph.entities.services.length

    graph.pasteBundle(bundle, { x: 0, y: 0 })
    expect(graph.entities.services.length).toBe(before + 1)

    graph.undo()
    expect(graph.entities.services.length).toBe(before)
    expect(graph.entities.routes).toHaveLength(1)
  })

  it('puts a dragged node back where it was', async () => {
    const graph = await freshStore()
    graph.setPosition('services:svc-1', { x: 10, y: 10 })
    graph.commitDrag([{ node: 'services:svc-1', position: { x: 400, y: 300 } }])
    expect(graph.positions['services:svc-1']).toEqual({ x: 400, y: 300 })

    graph.undo()
    expect(graph.positions['services:svc-1']).toEqual({ x: 10, y: 10 })
  })

  it('drops the redo trail once a new edit branches off it', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { host: 'a' })
    graph.undo()
    expect(graph.canRedo).toBe(true)

    graph.updateEntity('services', 'svc-1', { host: 'b' })
    expect(graph.canRedo).toBe(false)
  })

  it('has nothing to undo on a canvas straight from Kong', async () => {
    const graph = await freshStore()
    expect(graph.canUndo).toBe(false)
    expect(graph.undo()).toBeNull()
    expect(graph.redo()).toBeNull()
  })

  it('forgets its history when the canvas is reloaded from Kong', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { host: 'a' })
    expect(graph.canUndo).toBe(true)

    await graph.load('conn-1')
    expect(graph.canUndo).toBe(false)
    expect(graph.canRedo).toBe(false)
  })

  it('remembers a bounded number of steps', async () => {
    const graph = await freshStore()
    for (let i = 0; i < 60; i++) graph.updateEntity('services', 'svc-1', { port: 8000 + i })
    expect(graph.undoStack.length).toBe(50)
  })
})

describe('edits reaching the other canvases', () => {
  it('publishes what changed, as the value it now has', async () => {
    const graph = await freshStore()
    const { ops } = withPeer()

    graph.updateEntity('services', 'svc-1', { host: 'shared.internal' })

    const change = ops().at(-1).payload.changes[0]
    expect(change).toMatchObject({ kind: 'services', id: 'svc-1' })
    expect(change.value.host).toBe('shared.internal')
  })

  it('publishes a removal as an explicit null', async () => {
    const graph = await freshStore()
    const { ops } = withPeer()

    graph.deleteEntity('consumers', 'con-1')

    const change = ops().at(-1).payload.changes.find((c) => c.id === 'con-1')
    expect(change.value).toBeNull()
  })

  it('publishes an undo too, so taking something back reaches everyone', async () => {
    const graph = await freshStore()
    const { ops } = withPeer()

    graph.updateEntity('services', 'svc-1', { host: 'shared.internal' })
    graph.undo()

    expect(ops()).toHaveLength(2)
    expect(ops().at(-1).payload.changes[0].value.host).toBe('billing.internal')
  })

  it('applies somebody else\'s edit without putting it on this browser\'s undo stack', async () => {
    const graph = await freshStore()
    withPeer()

    graph.applyRemoteCanvasOp({
      id: 'tab-b',
      name: 'bob',
      data: { changes: [{ kind: 'services', id: 'svc-1', value: { id: 'svc-1', name: 'billing', host: 'bobs.internal' } }] },
    })

    expect(graph.entities.services[0].host).toBe('bobs.internal')
    // Ctrl+Z must take back your own work, never somebody else's.
    expect(graph.canUndo).toBe(false)
  })

  it('creates and removes entities on the receiving canvas', async () => {
    const graph = await freshStore()

    graph.applyRemoteCanvasOp({ data: { changes: [
      { kind: 'services', id: 'draft:from-bob', value: { id: 'draft:from-bob', name: 'bobs', host: 'b' } },
      { kind: 'consumers', id: 'con-1', value: null },
    ] } })

    expect(graph.entities.services.map((s) => s.id)).toContain('draft:from-bob')
    expect(graph.entities.consumers).toHaveLength(0)
  })

  it('does not echo a remote edit back out', async () => {
    const graph = await freshStore()
    const { ops } = withPeer()

    graph.applyRemoteCanvasOp({ data: { changes: [
      { kind: 'services', id: 'svc-1', value: { id: 'svc-1', name: 'billing', host: 'bobs.internal' } },
    ] } })

    expect(ops()).toHaveLength(0)
  })

  it('takes a whole canvas from a peer when joining, and starts clean', async () => {
    const graph = await freshStore()
    graph.updateEntity('services', 'svc-1', { host: 'mine' })

    graph.applyStateSync({ id: 'tab-b', name: 'bob', data: {
      entities: {
        services: [{ id: 'svc-1', name: 'billing', host: 'theirs.internal' }],
        routes: [], plugins: [], consumers: [], upstreams: [], targets: [],
      },
      positions: { 'services:svc-1': { x: 5, y: 6 } },
    } })

    expect(graph.entities.services[0].host).toBe('theirs.internal')
    expect(graph.positions['services:svc-1']).toEqual({ x: 5, y: 6 })
    // The draft is not this browser's doing, so there is nothing to undo.
    expect(graph.canUndo).toBe(false)
  })

  it('ignores a malformed sync rather than emptying the canvas', async () => {
    const graph = await freshStore()
    const before = JSON.stringify(graph.entities)

    graph.applyStateSync(null)
    graph.applyStateSync({ data: {} })
    graph.applyStateSync({ data: { entities: null } })

    expect(JSON.stringify(graph.entities)).toBe(before)
  })
})
