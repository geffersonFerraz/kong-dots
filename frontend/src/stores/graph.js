import { defineStore } from 'pinia'
import dagre from '@dagrejs/dagre'
import { api } from '../api/client'
import { deepClone } from '../api/clone'
import { DRAFT_PREFIX, KINDS, KIND_META, PLUGIN_PARENT_FIELD, entityLabel, isDraftId, nodeId, refId, splitNodeId, validateEntity } from '../api/entities'

const NODE_W = 210
const NODE_H = 76

const emptyState = () => Object.fromEntries(KINDS.map((k) => [k, []]))

let draftSeq = 0
const nextDraftId = (kind) => `${DRAFT_PREFIX}${kind}-${Date.now().toString(36)}-${++draftSeq}`

export const useGraphStore = defineStore('graph', {
  state: () => ({
    connectionId: null,
    info: null,
    baseline: emptyState(), // last known live state from Kong
    entities: emptyState(), // desired state being edited on the canvas
    positions: {}, // nodeId -> { x, y }
    fetchedAt: null,
    loading: false,
    error: null,
    selectedNodeId: null,
    // Canvas filter: matches on entity name and uuid, optionally narrowed to
    // some kinds. Non-matching nodes are dimmed, or hidden on request.
    filter: { query: '', kinds: [], hideUnmatched: false },
    pluginSchemas: {},
    plan: null,
    planning: false,
    applying: false,
    applyLog: [],
    lastApply: null,
    toast: null,
  }),

  getters: {
    availablePlugins: (s) => [...(s.info?.plugins ?? [])].sort(),

    byId: (s) => {
      const map = {}
      for (const kind of KINDS) for (const e of s.entities[kind] ?? []) map[nodeId(kind, e.id)] = e
      return map
    },

    selected: (s) => {
      if (!s.selectedNodeId) return null
      const { kind, id } = splitNodeId(s.selectedNodeId)
      const entity = (s.entities[kind] ?? []).find((e) => e.id === id)
      return entity ? { kind, id, entity } : null
    },

    allNodes: (s) => {
      const out = []
      for (const kind of KINDS) {
        for (const e of s.entities[kind] ?? []) {
          const nid = nodeId(kind, e.id)
          out.push({
            id: nid,
            type: 'kong',
            position: s.positions[nid] ?? { x: 0, y: 0 },
            data: { kind, entity: e, draft: isDraftId(e.id) },
          })
        }
      }
      return out
    },

    filterActive: (s) => s.filter.query.trim() !== '' || s.filter.kinds.length > 0,

    // Node ids matching the current filter. Empty set means "filter is off".
    matchedNodeIds() {
      if (!this.filterActive) return null
      const query = this.filter.query.trim().toLowerCase()
      const kinds = this.filter.kinds
      const out = new Set()
      for (const node of this.allNodes) {
        if (kinds.length && !kinds.includes(node.data.kind)) continue
        if (query && !matchesQuery(node.data.entity, query)) continue
        out.add(node.id)
      }
      return out
    },

    // What the search box lists, so a match can be jumped to directly.
    filterResults() {
      const matched = this.matchedNodeIds
      if (!matched) return []
      return this.allNodes
        .filter((n) => matched.has(n.id))
        .map((n) => ({
          nodeId: n.id,
          kind: n.data.kind,
          label: entityLabel(n.data.kind, n.data.entity),
          entityId: n.data.entity.id,
          draft: n.data.draft,
        }))
    },

    nodes() {
      const matched = this.matchedNodeIds
      const invalid = this.issuesByNode
      const decorate = (node) => {
        const issues = invalid[node.id]
        return issues ? { ...node, data: { ...node.data, issues } } : node
      }
      if (!matched) return this.allNodes.map(decorate)
      const hide = this.filter.hideUnmatched
      const out = []
      for (const node of this.allNodes) {
        const isMatch = matched.has(node.id)
        if (!isMatch && hide) continue
        const decorated = decorate(node)
        out.push(isMatch ? decorated : { ...decorated, data: { ...decorated.data, dimmed: true } })
      }
      return out
    },

    allEdges: (s) => {
      const out = []
      const exists = (kind, id) => (s.entities[kind] ?? []).some((e) => e.id === id)
      const push = (relation, sourceKind, sourceId, targetKind, targetId, extra = {}) => {
        if (!sourceId || !exists(sourceKind, sourceId)) return
        const source = nodeId(sourceKind, sourceId)
        const target = nodeId(targetKind, targetId)
        out.push({
          id: `${relation}|${source}|${target}`,
          source,
          target,
          type: 'smoothstep',
          animated: false,
          data: { relation },
          style: { stroke: KIND_META[sourceKind].accent, ...(extra.style ?? {}) },
          ...extra,
        })
      }

      for (const r of s.entities.routes ?? []) push('route-service', 'services', refId(r, 'service'), 'routes', r.id)
      for (const p of s.entities.plugins ?? []) {
        for (const [kind, field] of Object.entries(PLUGIN_PARENT_FIELD)) {
          const parent = refId(p, field)
          if (parent) push(`plugin-${field}`, kind, parent, 'plugins', p.id)
        }
      }
      for (const t of s.entities.targets ?? []) push('target-upstream', 'upstreams', refId(t, 'upstream'), 'targets', t.id)

      // A Service pointing at an Upstream does so by name, not by foreign key.
      const upstreamByName = new Map((s.entities.upstreams ?? []).map((u) => [u.name, u.id]))
      for (const svc of s.entities.services ?? []) {
        const upId = upstreamByName.get(svc.host)
        if (upId) push('service-upstream', 'upstreams', upId, 'services', svc.id, { style: { strokeDasharray: '6 4' } })
      }
      return out
    },

    edges() {
      const visible = new Set(this.nodes.map((n) => n.id))
      const matched = this.matchedNodeIds
      const out = []
      for (const edge of this.allEdges) {
        if (!visible.has(edge.source) || !visible.has(edge.target)) continue
        if (matched && !(matched.has(edge.source) && matched.has(edge.target))) {
          out.push({ ...edge, style: { ...edge.style, opacity: 0.15 } })
          continue
        }
        out.push(edge)
      }
      return out
    },

    // The entities this canvas would send to Kong, with the operation each one
    // would become. Used both for the "unsaved changes" badge and to decide
    // what is worth validating — an untouched entity came from Kong and is by
    // definition acceptable to it.
    pendingEntities: (s) => {
      const out = []
      for (const kind of KINDS) {
        const base = new Map((s.baseline[kind] ?? []).map((e) => [e.id, e]))
        for (const e of s.entities[kind] ?? []) {
          if (isDraftId(e.id) || !base.has(e.id)) {
            out.push({ type: 'create', kind, entity: e })
            base.delete(e.id)
            continue
          }
          const before = base.get(e.id)
          base.delete(e.id)
          if (JSON.stringify(before) !== JSON.stringify(e)) {
            out.push({ type: 'update', kind, entity: e })
          }
        }
        for (const [, e] of base) out.push({ type: 'delete', kind, entity: e })
      }
      return out
    },

    // A cheap local diff used for the "unsaved changes" badge. The authoritative
    // plan always comes from the backend, which re-reads Kong first.
    pending() {
      const summary = { create: 0, update: 0, delete: 0, items: [] }
      for (const { type, kind, entity } of this.pendingEntities) {
        summary[type]++
        summary.items.push({ type, kind, label: entityLabel(kind, entity) })
      }
      return summary
    },

    // Everything Kong would reject, found before an apply is attempted rather
    // than halfway through one.
    issues() {
      const out = []
      for (const { type, kind, entity } of this.pendingEntities) {
        if (type === 'delete') continue
        for (const issue of validateEntity(kind, entity)) {
          out.push({
            nodeId: nodeId(kind, entity.id),
            kind,
            entityId: entity.id,
            label: entityLabel(kind, entity),
            field: issue.field,
            message: issue.message,
          })
        }
      }
      return out
    },

    issuesByNode() {
      const out = {}
      for (const issue of this.issues) (out[issue.nodeId] ??= []).push(issue)
      return out
    },

    isDirty() {
      const p = this.pending
      return p.create + p.update + p.delete > 0
    },
  },

  actions: {
    setFilter(patch) {
      this.filter = { ...this.filter, ...patch }
    },

    clearFilter() {
      this.filter = { query: '', kinds: [], hideUnmatched: false }
    },

    notify(message, kind = 'info') {
      this.toast = { message, kind, at: Date.now() }
    },

    reset() {
      this.$patch({
        connectionId: null, info: null, baseline: emptyState(), entities: emptyState(),
        positions: {}, selectedNodeId: null, plan: null, applyLog: [], lastApply: null, error: null,
        filter: { query: '', kinds: [], hideUnmatched: false },
      })
    },

    async load(connectionId, { keepPositions = false } = {}) {
      this.loading = true
      this.error = null
      try {
        const res = await api.getState(connectionId)
        this.connectionId = connectionId
        this.info = res.info
        this.fetchedAt = res.fetched_at
        this.baseline = normalizeState(res.state)
        this.entities = deepClone(this.baseline)
        this.plan = null
        this.selectedNodeId = null
        const stored = {}
        for (const [key, pos] of Object.entries(res.layout ?? {})) stored[key] = { x: pos.x, y: pos.y }
        this.positions = keepPositions ? { ...stored, ...this.positions } : stored
        this.applyAutoLayout({ onlyMissing: true })
      } catch (e) {
        this.error = e.body?.kong_body ? `${e.message}` : e.message
      } finally {
        this.loading = false
      }
    },

    refresh() {
      if (this.connectionId) return this.load(this.connectionId)
    },

    // ------------------------------------------------------------- layout

    applyAutoLayout({ onlyMissing = false } = {}) {
      const g = new dagre.graphlib.Graph()
      g.setGraph({ rankdir: 'LR', nodesep: 34, ranksep: 110, marginx: 40, marginy: 40 })
      g.setDefaultEdgeLabel(() => ({}))
      const ids = new Set()
      for (const n of this.allNodes) {
        g.setNode(n.id, { width: NODE_W, height: NODE_H })
        ids.add(n.id)
      }
      for (const e of this.allEdges) if (ids.has(e.source) && ids.has(e.target)) g.setEdge(e.source, e.target)
      dagre.layout(g)
      const next = { ...this.positions }
      for (const id of ids) {
        if (onlyMissing && next[id]) continue
        const n = g.node(id)
        if (n) next[id] = { x: Math.round(n.x - NODE_W / 2), y: Math.round(n.y - NODE_H / 2) }
      }
      this.positions = next
    },

    setPosition(nid, pos) {
      this.positions = { ...this.positions, [nid]: { x: Math.round(pos.x), y: Math.round(pos.y) } }
    },

    async persistLayout() {
      if (!this.connectionId) return
      const positions = Object.entries(this.positions)
        .map(([nid, pos]) => {
          const { kind, id } = splitNodeId(nid)
          return { entity_type: kind, entity_id: id, x: pos.x, y: pos.y }
        })
        .filter((p) => p.entity_id && !isDraftId(p.entity_id))
      if (!positions.length) return
      try {
        await api.saveLayout(this.connectionId, positions)
      } catch (e) {
        this.notify(`Could not save layout: ${e.message}`, 'error')
      }
    },

    // ----------------------------------------------------------- mutation

    createEntity(kind, patch = {}, position = null) {
      const entity = { ...KIND_META[kind].defaults(), ...patch, id: nextDraftId(kind) }
      this.entities[kind] = [...this.entities[kind], entity]
      const nid = nodeId(kind, entity.id)
      if (position) this.setPosition(nid, position)
      this.selectedNodeId = nid
      return entity
    },

    updateEntity(kind, id, patch) {
      this.entities[kind] = this.entities[kind].map((e) => (e.id === id ? { ...e, ...patch } : e))
    },

    // cascade lists everything that would disappear along with an entity, so the
    // confirmation dialog can show it before anything is removed.
    cascade(kind, id) {
      const out = [{ kind, id, label: entityLabel(kind, this.entities[kind].find((e) => e.id === id)) }]
      const addPlugins = (field, parentId) => {
        for (const p of this.entities.plugins) {
          if (refId(p, field) === parentId) out.push({ kind: 'plugins', id: p.id, label: entityLabel('plugins', p) })
        }
      }
      if (kind === 'services') {
        addPlugins('service', id)
        for (const r of this.entities.routes) {
          if (refId(r, 'service') === id) {
            out.push({ kind: 'routes', id: r.id, label: entityLabel('routes', r) })
            addPlugins('route', r.id)
          }
        }
      } else if (kind === 'routes') {
        addPlugins('route', id)
      } else if (kind === 'consumers') {
        addPlugins('consumer', id)
      } else if (kind === 'upstreams') {
        for (const t of this.entities.targets) {
          if (refId(t, 'upstream') === id) out.push({ kind: 'targets', id: t.id, label: entityLabel('targets', t) })
        }
      }
      return out
    },

    deleteEntity(kind, id) {
      for (const victim of this.cascade(kind, id)) {
        this.entities[victim.kind] = this.entities[victim.kind].filter((e) => e.id !== victim.id)
        const nid = nodeId(victim.kind, victim.id)
        if (this.selectedNodeId === nid) this.selectedNodeId = null
        delete this.positions[nid]
      }
    },

    // connect translates a canvas link into the foreign key Kong actually uses.
    // It returns an error string when the two kinds cannot be related.
    connect(sourceNid, targetNid) {
      if (!sourceNid || !targetNid || sourceNid === targetNid) return 'Invalid connection'
      const src = splitNodeId(sourceNid)
      const dst = splitNodeId(targetNid)

      if (dst.kind === 'plugins') {
        const field = PLUGIN_PARENT_FIELD[src.kind]
        if (!field) return `A Plugin cannot be attached to a ${KIND_META[src.kind].singular}`
        // A plugin has exactly one owner, so attaching to a new one detaches it.
        this.updateEntity('plugins', dst.id, { service: null, route: null, consumer: null, [field]: { id: src.id } })
        return null
      }
      if (dst.kind === 'routes' && src.kind === 'services') {
        this.updateEntity('routes', dst.id, { service: { id: src.id } })
        return null
      }
      if (dst.kind === 'targets' && src.kind === 'upstreams') {
        this.updateEntity('targets', dst.id, { upstream: { id: src.id } })
        return null
      }
      if (dst.kind === 'services' && src.kind === 'upstreams') {
        const upstream = this.entities.upstreams.find((u) => u.id === src.id)
        if (!upstream?.name) return 'Name the Upstream before pointing a Service at it'
        this.updateEntity('services', dst.id, { host: upstream.name })
        return null
      }
      return `${KIND_META[src.kind].singular} → ${KIND_META[dst.kind].singular} is not a relation Kong supports`
    },

    disconnect(edge) {
      const relation = edge.data?.relation ?? edge.id.split('|')[0]
      const dst = splitNodeId(edge.target)
      switch (relation) {
        case 'route-service':
          return this.updateEntity('routes', dst.id, { service: null })
        case 'plugin-service':
          return this.updateEntity('plugins', dst.id, { service: null })
        case 'plugin-route':
          return this.updateEntity('plugins', dst.id, { route: null })
        case 'plugin-consumer':
          return this.updateEntity('plugins', dst.id, { consumer: null })
        case 'target-upstream':
          return this.updateEntity('targets', dst.id, { upstream: null })
        case 'service-upstream':
          return this.updateEntity('services', dst.id, { host: '' })
      }
    },

    discardChanges() {
      this.entities = deepClone(this.baseline)
      this.plan = null
      this.selectedNodeId = null
    },

    // ------------------------------------------------------------ schemas

    async loadPluginSchema(name) {
      if (!name || this.pluginSchemas[name]) return this.pluginSchemas[name]
      try {
        const schema = await api.pluginSchema(this.connectionId, name)
        this.pluginSchemas = { ...this.pluginSchemas, [name]: schema }
        return schema
      } catch (e) {
        this.notify(`Schema for "${name}" unavailable: ${e.message}`, 'error')
        return null
      }
    },

    // -------------------------------------------------------- plan / apply

    async buildPlan() {
      this.planning = true
      try {
        this.plan = await api.plan(this.connectionId, this.desiredPayload())
        return this.plan
      } catch (e) {
        this.notify(`Could not build the plan: ${e.message}`, 'error')
        return null
      } finally {
        this.planning = false
      }
    },

    async apply() {
      this.applying = true
      this.applyLog = []
      try {
        const res = await api.apply(this.connectionId, this.desiredPayload())
        this.lastApply = res.result
        const ops = res.plan?.ops ?? []
        const idMap = res.result?.id_map ?? {}

        this.rekeyPositions(ops, idMap)
        this.rekeyEntities(idMap)
        await this.persistLayout()

        if (res.result.status === 'success') {
          this.notify(`Applied ${ops.length} change(s) to Kong`, 'success')
          await this.load(this.connectionId, { keepPositions: true })
        } else {
          // A failed run must not cost the user their work: reload the live
          // state as the new baseline, but keep every entity whose operation
          // did not go through, so the canvas still holds the pending changes.
          this.notify(res.result.error ?? 'Apply finished with errors', 'error')
          await this.reloadKeepingUnapplied(appliedKeys(ops, res.result?.results ?? [], idMap))
          // Re-plan so the review panel shows what is still outstanding instead
          // of claiming there is nothing left to do.
          await this.buildPlan()
          return res
        }
        this.plan = null
        return res
      } catch (e) {
        // The request itself failed, so nothing is known to have changed —
        // leave the canvas exactly as the user left it.
        this.notify(`Apply failed: ${e.message}`, 'error')
        return null
      } finally {
        this.applying = false
      }
    },

    // rekeyEntities rewrites draft ids that Kong has now assigned real ids to,
    // both on the entities themselves and on anything referring to them.
    rekeyEntities(idMap) {
      if (!Object.keys(idMap).length) return
      const next = emptyState()
      for (const kind of KINDS) {
        next[kind] = (this.entities[kind] ?? []).map((entity) => {
          const out = { ...entity }
          if (idMap[out.id]) out.id = idMap[out.id]
          for (const field of REF_FIELDS) {
            const id = refId(out, field)
            if (id && idMap[id]) out[field] = { id: idMap[id] }
          }
          return out
        })
      }
      this.entities = next
    },

    // reloadKeepingUnapplied refreshes the baseline from Kong while keeping the
    // canvas entities whose operation failed or was skipped. Entities that were
    // applied are replaced by Kong's own version, so no phantom diff remains.
    async reloadKeepingUnapplied(applied) {
      const res = await api.getState(this.connectionId)
      const live = normalizeState(res.state)
      const next = emptyState()
      for (const kind of KINDS) {
        const liveById = new Map(live[kind].map((e) => [e.id, e]))
        for (const local of this.entities[kind] ?? []) {
          const fromKong = liveById.get(local.id)
          next[kind].push(fromKong && applied.has(`${kind}:${local.id}`) ? fromKong : local)
        }
      }
      this.baseline = live
      this.entities = next
      this.info = res.info
      this.fetchedAt = res.fetched_at
      const stored = {}
      for (const [key, pos] of Object.entries(res.layout ?? {})) stored[key] = { x: pos.x, y: pos.y }
      this.positions = { ...stored, ...this.positions }
      this.applyAutoLayout({ onlyMissing: true })
    },

    // rekeyPositions moves the canvas position of a node that was just created
    // from its draft id to the id Kong assigned, so it stays where the user put it.
    rekeyPositions(ops, idMap) {
      if (!Object.keys(idMap).length) return
      const next = { ...this.positions }
      for (const op of ops) {
        const realId = idMap[op.entity_id]
        if (!realId) continue
        const from = nodeId(op.kind, op.entity_id)
        if (next[from]) {
          next[nodeId(op.kind, realId)] = next[from]
          delete next[from]
        }
      }
      this.positions = next
    },

    // desiredPayload strips canvas-only fields before the state is diffed.
    desiredPayload() {
      const out = {}
      for (const kind of KINDS) out[kind] = (this.entities[kind] ?? []).map(stripRuntimeFields)
      return out
    },

    async exportDeck() {
      return api.exportDeck(this.connectionId)
    },

    async importDeck(yaml) {
      const res = await api.importDeck(this.connectionId, yaml)
      this.entities = normalizeState(res.state)
      this.positions = {}
      this.applyAutoLayout()
      this.notify('decK file loaded onto the canvas — review the diff before applying', 'info')
    },

    recordApplyEvent(event) {
      this.applyLog = [...this.applyLog, event].slice(-200)
    },
  },
})

// Foreign-key fields that can point at an entity created in the same apply.
const REF_FIELDS = ['service', 'route', 'consumer', 'upstream']

// appliedKeys collects the entities Kong actually accepted, keyed by the id
// they now have.
function appliedKeys(ops, results, idMap) {
  const out = new Set()
  ops.forEach((op, i) => {
    if (results[i]?.status !== 'ok') return
    out.add(`${op.kind}:${idMap[op.entity_id] ?? op.entity_id}`)
  })
  return out
}

// matchesQuery searches the fields a user would type: the entity name (whatever
// the kind calls it) and the Kong uuid, both as case-insensitive substrings.
function matchesQuery(entity, query) {
  for (const key of ['name', 'username', 'target', 'id']) {
    const value = entity?.[key]
    if (typeof value === 'string' && value.toLowerCase().includes(query)) return true
  }
  return false
}

function normalizeState(state) {
  const out = emptyState()
  for (const kind of KINDS) out[kind] = deepClone(state?.[kind] ?? [])
  return out
}

// Kong reports these but never accepts them back.
const RUNTIME_FIELDS = ['created_at', 'updated_at', 'ws_id']

function stripRuntimeFields(entity) {
  const out = {}
  for (const [k, v] of Object.entries(entity)) {
    if (RUNTIME_FIELDS.includes(k)) continue
    out[k] = v
  }
  return out
}
