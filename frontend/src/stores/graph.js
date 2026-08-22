import { defineStore } from 'pinia'
import dagre from '@dagrejs/dagre'
import { api } from '../api/client'
import { deepClone } from '../api/clone'
import { useSessionStore } from './session'
import { DRAFT_PREFIX, KINDS, KIND_META, PLUGIN_PARENT_FIELD, entityLabel, isDraftId, nodeId, refId, splitNodeId, uniqueKeys, validateEntity } from '../api/entities'

const NODE_W = 210
const NODE_H = 76

const emptyState = () => Object.fromEntries(KINDS.map((k) => [k, []]))

let draftSeq = 0
const nextDraftId = (kind) => `${DRAFT_PREFIX}${kind}-${Date.now().toString(36)}-${++draftSeq}`

// How many edits back Ctrl+Z reaches.
const UNDO_DEPTH = 50

// Set while an edit is being recorded, so a mutation that calls another one is
// still a single action.
let committing = false

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
    // Set when this canvas's changes were filed for somebody else to approve.
    filedRequest: null,
    // Set when somebody else's change landed in Kong while this canvas was open.
    remoteChange: null,
    // Nodes this browser is dragging right now. A remote move for one of them
    // is ignored: two people cannot both decide where it goes mid-drag.
    localDrag: [],
    // Canvas edits this browser made, newest last, and the ones it has undone.
    // Undo is local: Ctrl+Z takes back what *you* did, not the last thing that
    // happened on the shared canvas.
    undoStack: [],
    redoStack: [],
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
        if (query && !matchesQuery(node.data.kind, node.data.entity, query)) continue
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
          detail: describeEntity(n.data.kind, n.data.entity),
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
      const occupants = this.uniqueKeyOwners
      for (const { type, kind, entity } of this.pendingEntities) {
        if (type === 'delete') continue
        const report = (issue) =>
          out.push({
            nodeId: nodeId(kind, entity.id),
            kind,
            entityId: entity.id,
            label: entityLabel(kind, entity),
            field: issue.field,
            message: issue.message,
          })

        validateEntity(kind, entity).forEach(report)

        // Kong enforces uniqueness with a 409 mid-apply; say it here instead.
        for (const { key, field, what } of uniqueKeys(kind, entity)) {
          const others = (occupants[key] ?? []).filter((o) => o.id !== entity.id)
          if (!others.length) continue
          report({ field, message: this.duplicateMessage(kind, entity, others[0], what) })
        }
      }
      return out
    },

    // Every entity that claims each uniqueness key, so a collision can name the
    // entity already holding it.
    uniqueKeyOwners: (s) => {
      const out = {}
      for (const kind of KINDS) {
        for (const entity of s.entities[kind] ?? []) {
          for (const { key } of uniqueKeys(kind, entity)) {
            (out[key] ??= []).push({ id: entity.id, kind, entity })
          }
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

    canUndo: (s) => s.undoStack.length > 0,
    canRedo: (s) => s.redoStack.length > 0,
    undoLabel: (s) => s.undoStack.at(-1)?.label ?? '',
    redoLabel: (s) => s.redoStack.at(-1)?.label ?? '',

    // Entities somebody else changed in Kong after this canvas read them.
    // Applying over them would quietly undo their work.
    conflicts: (s) => s.plan?.conflicts ?? [],

    // Entities that appeared in Kong after this canvas loaded. They are left
    // alone rather than deleted, and shown so nobody wonders where they went.
    ignored: (s) => s.plan?.ignored ?? [],
  },

  actions: {
    // duplicateMessage explains a uniqueness clash in Kong's own terms.
    duplicateMessage(kind, entity, other, what) {
      if (kind !== 'plugins') {
        const singular = KIND_META[kind].singular
        const owner = isDraftId(other.id) ? 'another new' : 'an existing'
        return `${owner} ${singular} already uses the ${what} "${entityLabel(kind, entity)}"`
      }
      const scope = this.pluginScopeLabel(entity)
      return `${scope} already has a ${entity.name} plugin — Kong allows only one of each per scope`
    },

    // pluginScopeLabel names what a plugin is attached to, for error messages.
    pluginScopeLabel(plugin) {
      for (const [field, kind] of [
        ['service', 'services'],
        ['route', 'routes'],
        ['consumer', 'consumers'],
      ]) {
        const id = refId(plugin, field)
        if (!id) continue
        const parent = (this.entities[kind] ?? []).find((e) => e.id === id)
        return `${KIND_META[kind].singular} ${parent ? entityLabel(kind, parent) : id}`
      }
      return 'This gateway'
    },

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
        filedRequest: null, remoteChange: null, undoStack: [], redoStack: [],
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
        this.remoteChange = null
        this.undoStack = []
        this.redoStack = []
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

    applyAutoLayout(opts = {}) {
      if (opts.onlyMissing) return this.layoutInner(opts)
      return this.commit('Auto-layout', () => this.layoutInner(opts))
    },

    // commitDrag records where nodes ended up, as one undoable step per drag.
    commitDrag(moved) {
      this.commit('Move node', () => {
        for (const { node, position } of moved ?? []) this.setPosition(node, position)
      })
    },

    layoutInner({ onlyMissing = false } = {}) {
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

    beginLocalDrag(nodeIds) {
      this.localDrag = nodeIds ?? []
    },

    endLocalDrag() {
      this.localDrag = []
    },

    // applyRemotePosition moves a node because somebody else is dragging it.
    // Layout is shared per Kong — it lives in canvas_layout, not per user — so
    // their drag is the canvas's new truth; it just arrives live now instead of
    // at the next reload. Only the person dragging persists it, on drop.
    applyRemotePosition(payload) {
      if (!payload?.node || this.localDrag.includes(payload.node)) return
      this.setPosition(payload.node, { x: payload.x, y: payload.y })
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

    // ------------------------------------------------- shared draft + undo

    // commit is the single funnel every canvas edit goes through. It works out
    // what the edit changed, keeps that so it can be taken back, and hands the
    // same list to everyone else on this Kong — which is what makes one
    // person's change show up on the others' canvas straight away.
    //
    // Nested calls fold into the outermost one, so `connect`, which edits
    // through updateEntity, is one action to undo rather than two.
    commit(label, fn) {
      if (committing) return fn()
      committing = true
      const beforeEntities = entityMap(deepClone(this.entities))
      const beforePositions = { ...this.positions }
      let result
      try {
        result = fn()
      } finally {
        committing = false
      }

      const changes = diffEntities(beforeEntities, entityMap(deepClone(this.entities)))
      const positions = diffPositions(beforePositions, this.positions)
      if (!changes.length && !positions.length) return result

      this.undoStack = [...this.undoStack, { label, changes, positions }].slice(-UNDO_DEPTH)
      // A new edit is a new branch: whatever was undone cannot be redone onto it.
      this.redoStack = []
      this.publish(sideOf(changes, 'after'), positionSide(positions, 'after'))
      return result
    },

    // publish sends an edit to the other canvases. Applying it there goes
    // straight into the entities, never back through commit, or the two
    // browsers would bounce the same change between them forever.
    publish(values, positions) {
      const session = useSessionStore()
      session.sendCanvasOp(values, positions)
    },

    undo() {
      const action = this.undoStack.at(-1)
      if (!action) return null
      this.undoStack = this.undoStack.slice(0, -1)
      this.redoStack = [...this.redoStack, action]
      this.applySide(action, 'before')
      return action
    },

    redo() {
      const action = this.redoStack.at(-1)
      if (!action) return null
      this.redoStack = this.redoStack.slice(0, -1)
      this.undoStack = [...this.undoStack, action]
      this.applySide(action, 'after')
      return action
    },

    // applySide moves the canvas to one side of a recorded action and tells
    // everyone else, so an undo travels exactly like the edit it takes back.
    applySide(action, side) {
      const values = sideOf(action.changes, side)
      const positions = positionSide(action.positions ?? [], side)
      this.applyEntityValues(values)
      this.applyPositions(positions)
      this.publish(values, positions)
    },

    // applyEntityValues is where a change actually lands, whoever it came from:
    // this browser, an undo, or somebody else's canvas. A null value removes.
    applyEntityValues(values) {
      if (!values?.length) return
      const next = { ...this.entities }
      for (const { kind, id, value } of values) {
        if (!KINDS.includes(kind) || !id) continue
        const current = next[kind] ?? []
        if (value === null || value === undefined) {
          next[kind] = current.filter((e) => e.id !== id)
          const nid = nodeId(kind, id)
          if (this.selectedNodeId === nid) this.selectedNodeId = null
          delete this.positions[nid]
          continue
        }
        const at = current.findIndex((e) => e.id === id)
        next[kind] = at === -1 ? [...current, value] : current.map((e) => (e.id === id ? value : e))
      }
      this.entities = next
    },

    applyPositions(positions) {
      if (!positions?.length) return
      const next = { ...this.positions }
      for (const p of positions) {
        if (!p?.node) continue
        if (p.x === undefined || p.x === null) delete next[p.node]
        else next[p.node] = { x: Math.round(p.x), y: Math.round(p.y) }
      }
      this.positions = next
    },

    // applyRemoteCanvasOp takes somebody else's edit. It does not touch this
    // browser's undo stack: Ctrl+Z takes back your own work, never theirs.
    applyRemoteCanvasOp(payload) {
      const data = payload?.data ?? payload
      if (!data) return
      this.applyEntityValues(data.changes ?? [])
      this.applyPositions(data.positions ?? [])
    },

    // canvasSnapshot is the whole shared draft, for handing to a tab that has
    // just opened this Kong and would otherwise see only what Kong reports.
    canvasSnapshot() {
      return { entities: deepClone(this.entities), positions: { ...this.positions } }
    },

    applyStateSync(payload) {
      const data = payload?.data ?? payload
      if (!data?.entities) return
      this.entities = normalizeState(data.entities)
      if (data.positions) this.positions = { ...this.positions, ...data.positions }
      // The draft did not come from anything this browser did, so there is
      // nothing here for it to undo.
      this.undoStack = []
      this.redoStack = []
    },

    // ----------------------------------------------------------- mutation

    createEntity(kind, patch = {}, position = null) {
      return this.commit(`Add ${KIND_META[kind].singular}`, () => {
        const entity = { ...KIND_META[kind].defaults(), ...patch, id: nextDraftId(kind) }
        this.entities[kind] = [...this.entities[kind], entity]
        const nid = nodeId(kind, entity.id)
        if (position) this.setPosition(nid, position)
        this.selectedNodeId = nid
        return entity
      })
    },

    updateEntity(kind, id, patch) {
      this.commit(`Edit ${KIND_META[kind].singular}`, () => {
        this.entities[kind] = this.entities[kind].map((e) => (e.id === id ? { ...e, ...patch } : e))
      })
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
      const label = entityLabel(kind, (this.entities[kind] ?? []).find((e) => e.id === id))
      this.commit(`Delete ${KIND_META[kind].singular} ${label}`, () => {
        for (const victim of this.cascade(kind, id)) {
          this.entities[victim.kind] = this.entities[victim.kind].filter((e) => e.id !== victim.id)
          const nid = nodeId(victim.kind, victim.id)
          if (this.selectedNodeId === nid) this.selectedNodeId = null
          delete this.positions[nid]
        }
      })
    },

    // connect translates a canvas link into the foreign key Kong actually uses.
    // It returns an error string when the two kinds cannot be related.
    connect(sourceNid, targetNid) {
      return this.commit('Connect', () => this.connectInner(sourceNid, targetNid))
    },

    connectInner(sourceNid, targetNid) {
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
      return this.commit('Disconnect', () => this.disconnectInner(edge))
    },

    disconnectInner(edge) {
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

    // ---------------------------------------------------------- clipboard

    // copyClosure lists everything that has to travel with an entity for the
    // copy to be worth anything on the other side. It is deliberately not
    // `cascade`: deleting a Service must leave the Upstream it points at alone,
    // but copying one without that Upstream lands a Service in the other Kong
    // whose host nothing answers on.
    copyClosure(kind, id) {
      const out = []
      const seen = new Set()
      const find = (k, i) => (this.entities[k] ?? []).find((e) => e.id === i)
      const add = (k, entity) => {
        if (!entity || seen.has(`${k}:${entity.id}`)) return false
        seen.add(`${k}:${entity.id}`)
        out.push({ kind: k, id: entity.id, label: entityLabel(k, entity) })
        return true
      }
      const addPlugins = (field, parentId) => {
        for (const p of this.entities.plugins ?? []) if (refId(p, field) === parentId) add('plugins', p)
      }
      const addTargets = (upstreamId) => {
        for (const t of this.entities.targets ?? []) if (refId(t, 'upstream') === upstreamId) add('targets', t)
      }
      const addUpstream = (upstream) => {
        if (add('upstreams', upstream)) addTargets(upstream.id)
      }
      const addRoute = (route) => {
        if (add('routes', route)) addPlugins('route', route.id)
      }
      const addService = (service) => {
        if (!add('services', service)) return
        addPlugins('service', service.id)
        for (const r of this.entities.routes ?? []) if (refId(r, 'service') === service.id) addRoute(r)
        // A Service names its Upstream instead of referencing it, so the link
        // survives the copy only if the Upstream comes along under that name.
        addUpstream((this.entities.upstreams ?? []).find((u) => u.name && u.name === service.host))
      }

      const root = find(kind, id)
      if (!root) return out
      switch (kind) {
        case 'services':
          addService(root)
          break
        case 'routes':
          addRoute(root)
          break
        case 'consumers':
          if (add('consumers', root)) addPlugins('consumer', root.id)
          break
        case 'upstreams':
          addUpstream(root)
          break
        default:
          add(kind, root)
      }
      return out
    },

    // clipboardBundle serialises an entity and everything that belongs to it —
    // a Service carries its Routes, every plugin on them and the Upstream it
    // points at. Ids are replaced by placeholders and references that point
    // outside the bundle are dropped, because the target is usually a different
    // Kong where those ids mean nothing.
    clipboardBundle(kind, id) {
      const members = this.copyClosure(kind, id)
      const inBundle = new Map(members.map((m) => [`${m.kind}:${m.id}`, m]))

      const idMap = {}
      members.forEach((m, i) => (idMap[m.id] = `${DRAFT_PREFIX}paste-${i + 1}`))

      const entities = emptyState()
      for (const member of members) {
        const source = (this.entities[member.kind] ?? []).find((e) => e.id === member.id)
        if (!source) continue
        const copy = {}
        for (const [key, value] of Object.entries(stripRuntimeFields(source))) copy[key] = deepClone(value)
        copy.id = idMap[member.id]
        for (const field of REF_FIELDS) {
          const ref = refId(copy, field)
          if (!ref) continue
          const target = REF_KIND[field]
          copy[field] = inBundle.has(`${target}:${ref}`) ? { id: idMap[ref] } : null
        }
        entities[member.kind].push(copy)
      }

      const root = (this.entities[kind] ?? []).find((e) => e.id === id)
      return {
        kong_flow: 1,
        kind,
        label: entityLabel(kind, root),
        copied_at: new Date().toISOString(),
        entities,
      }
    },

    // resolveNameClashes finds free names for a bundle about to be pasted. Kong
    // makes names unique, so pasting a Service next to the one it was copied
    // from is a 409 waiting to happen unless the copy is renamed here first.
    // Returns the field patches per entity, plus the old→new Upstream names so
    // Services can keep pointing at the right one.
    resolveNameClashes(bundle) {
      const claimed = new Set()
      const claim = (kind, field, value) => value && claimed.add(`${kind}:${field}:${value}`)
      const isTaken = (kind, field, value) => claimed.has(`${kind}:${field}:${value}`)
      for (const kind of KINDS) {
        for (const entity of this.entities[kind] ?? []) {
          for (const field of UNIQUE_NAME_FIELDS[kind] ?? []) claim(kind, field, entity[field])
        }
      }

      const free = (kind, field, original) => {
        let name = original
        let n = 0
        while (isTaken(kind, field, name)) {
          n += 1
          name = n === 1 ? `${original}-copy` : `${original}-copy-${n}`
        }
        claim(kind, field, name)
        return name
      }

      const names = {}
      const hosts = {}
      for (const kind of KINDS) {
        const fields = UNIQUE_NAME_FIELDS[kind] ?? []
        if (!fields.length) continue
        for (const entity of bundle.entities[kind] ?? []) {
          const patch = {}
          for (const field of fields) {
            const original = entity?.[field]
            if (!original) continue
            const name = free(kind, field, original)
            if (name === original) continue
            patch[field] = name
            if (kind === 'upstreams' && field === 'name') hosts[original] = name
          }
          if (Object.keys(patch).length) names[`${kind}:${entity.id}`] = patch
        }
      }
      return { names, hosts }
    },

    // pasteBundle turns a bundle back into draft entities. Fresh ids are minted
    // so the same clipboard can be pasted repeatedly without colliding.
    pasteBundle(bundle, origin = { x: 80, y: 80 }) {
      if (!bundle || bundle.kong_flow !== 1 || typeof bundle.entities !== 'object') {
        throw new Error('that does not look like something copied from Kong Flow')
      }
      return this.commit(`Paste ${bundle.label ?? 'entities'}`, () => this.pasteInner(bundle, origin))
    },

    pasteInner(bundle, origin) {
      const idMap = {}
      for (const kind of KINDS) {
        for (const entity of bundle.entities[kind] ?? []) {
          if (entity?.id) idMap[entity.id] = nextDraftId(kind)
        }
      }
      const { names, hosts } = this.resolveNameClashes(bundle)

      const created = {}
      let column = 0
      for (const kind of KINDS) {
        const list = bundle.entities[kind] ?? []
        if (!list.length) continue
        list.forEach((entity, row) => {
          const copy = deepClone(entity)
          copy.id = idMap[entity.id] ?? nextDraftId(kind)
          const renamed = names[`${kind}:${entity.id}`]
          if (renamed) Object.assign(copy, renamed)
          // A Service points at its Upstream by name, so a renamed Upstream has
          // to be followed here or the pasted Service loses its balancer.
          if (kind === 'services' && hosts[copy.host]) copy.host = hosts[copy.host]
          for (const field of REF_FIELDS) {
            const ref = refId(copy, field)
            copy[field] = ref && idMap[ref] ? { id: idMap[ref] } : null
            if (copy[field] === null && !ref) delete copy[field]
          }
          this.entities[kind] = [...this.entities[kind], copy]
          this.setPosition(nodeId(kind, copy.id), { x: origin.x + column * 260, y: origin.y + row * 96 })
        })
        created[kind] = list.length
        column++
      }

      const first = KINDS.map((k) => (bundle.entities[k] ?? [])[0]).find(Boolean)
      if (first) this.selectedNodeId = nodeId(KINDS.find((k) => (bundle.entities[k] ?? []).length), idMap[first.id])
      return created
    },

    discardChanges() {
      this.commit('Discard changes', () => {
        this.entities = deepClone(this.baseline)
        this.plan = null
        this.selectedNodeId = null
      })
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
        this.plan = await api.plan(this.connectionId, this.changePayload())
        return this.plan
      } catch (e) {
        this.notify(`Could not build the plan: ${e.message}`, 'error')
        return null
      } finally {
        this.planning = false
      }
    },

    // apply sends the canvas to the backend. What happens there depends on who
    // is asking: an approver's changes go to Kong, everybody else's are filed
    // for review and nothing is sent to the gateway.
    async apply({ force = false, title = '' } = {}) {
      this.applying = true
      this.applyLog = []
      try {
        const res = await api.apply(this.connectionId, { ...this.changePayload(), force, title })

        if (res?.status === 'pending_approval') {
          this.filedRequest = res.request
          this.plan = null
          this.notify('Filed for approval — nothing has been sent to Kong yet', 'info')
          return res
        }

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
        // A 409 means Kong moved underneath this canvas. The backend sends back
        // the plan it refused to run, drift included, so the review panel can
        // show exactly whose change is in the way.
        if (e?.status === 409 && e.body?.plan) {
          this.plan = e.body.plan
          this.notify(e.message, 'error')
          return null
        }
        // The request itself failed, so nothing is known to have changed —
        // leave the canvas exactly as the user left it.
        this.notify(`Apply failed: ${e.message}`, 'error')
        return null
      } finally {
        this.applying = false
      }
    },

    // noteRemoteChange records that somebody else's change reached this Kong,
    // so the canvas can offer a refresh instead of silently going stale.
    noteRemoteChange(payload) {
      const session = useSessionStore()
      if (!payload || payload.by === session.clientId) return
      this.remoteChange = { actor: payload.actor ?? 'somebody', summary: payload.summary ?? null, at: Date.now() }
    },

    dismissRemoteChange() {
      this.remoteChange = null
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
      return statePayload(this.entities)
    },

    // baselinePayload is what Kong looked like when this canvas loaded. The
    // backend needs it to tell "the user removed this" apart from "somebody
    // else added this while the canvas was open" — without it, a canvas that
    // has been open for ten minutes deletes everything created since.
    baselinePayload() {
      return statePayload(this.baseline)
    },

    // changePayload is the whole proposal: the canvas, what it was built on,
    // and who is proposing it.
    changePayload() {
      const session = useSessionStore()
      return {
        desired: this.desiredPayload(),
        baseline: this.baselinePayload(),
        actor: session.displayName,
        client_id: session.clientId,
      }
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

// The free-form names Kong keeps unique. A pasted copy has to move off any of
// these that the target Kong already uses. Plugin names (the plugin type) and
// Target addresses are not free-form, so renaming them would be nonsense.
const UNIQUE_NAME_FIELDS = {
  services: ['name'],
  routes: ['name'],
  consumers: ['username', 'custom_id'],
  upstreams: ['name'],
}

// Foreign-key fields that can point at an entity created in the same apply.
const REF_FIELDS = ['service', 'route', 'consumer', 'upstream']

// Which kind each foreign key points at.
const REF_KIND = { service: 'services', route: 'routes', consumer: 'consumers', upstream: 'upstreams' }

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

// describeEntity renders the same one-line summary the node shows, so a result
// row makes it obvious which host or path matched.
function describeEntity(kind, entity) {
  try {
    return KIND_META[kind]?.subtitle?.(entity) ?? ''
  } catch {
    return ''
  }
}

// matchesQuery searches the fields a user would actually type for that kind —
// a Service by host, a Route by path — plus the Kong uuid, which every entity
// has. Matching is case-insensitive and by substring; list fields match when
// any of their entries does.
function matchesQuery(kind, entity, query) {
  if (typeof entity?.id === 'string' && entity.id.toLowerCase().includes(query)) return true
  for (const key of KIND_META[kind]?.searchFields ?? ['name']) {
    const value = entity?.[key]
    if (typeof value === 'string') {
      if (value.toLowerCase().includes(query)) return true
    } else if (Array.isArray(value)) {
      if (value.some((v) => typeof v === 'string' && v.toLowerCase().includes(query))) return true
    }
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

// entityMap flattens a canvas into key -> entity, which is what makes an edit
// expressible as a list of entities that changed.
function entityMap(state) {
  const out = new Map()
  for (const kind of KINDS) for (const e of state?.[kind] ?? []) out.set(nodeId(kind, e.id), e)
  return out
}

// diffEntities works out what an edit did, one entry per entity touched. A null
// on either side means the entity was not there: `after: null` is a removal,
// `before: null` a creation.
function diffEntities(before, after) {
  const changes = []
  for (const [key, entity] of after) {
    const prev = before.get(key)
    if (prev && JSON.stringify(prev) === JSON.stringify(entity)) continue
    const { kind, id } = splitNodeId(key)
    changes.push({ kind, id, before: prev ?? null, after: entity })
  }
  for (const [key, entity] of before) {
    if (after.has(key)) continue
    const { kind, id } = splitNodeId(key)
    changes.push({ kind, id, before: entity, after: null })
  }
  return changes
}

function diffPositions(before, after) {
  const moved = []
  const keys = new Set([...Object.keys(before), ...Object.keys(after)])
  for (const node of keys) {
    const from = before[node]
    const to = after[node]
    if (from && to && from.x === to.x && from.y === to.y) continue
    if (!from && !to) continue
    moved.push({ node, before: from ?? null, after: to ?? null })
  }
  return moved
}

// sideOf turns recorded changes into the flat list that goes over the wire and
// into applyEntityValues: which entity, and what it should now be.
function sideOf(changes, side) {
  return changes.map((c) => ({ kind: c.kind, id: c.id, value: c[side] }))
}

function positionSide(positions, side) {
  return positions.map((p) => ({ node: p.node, ...(p[side] ?? { x: null, y: null }) }))
}

// statePayload renders a whole state the way the backend wants to read it.
function statePayload(state) {
  const out = {}
  for (const kind of KINDS) out[kind] = (state?.[kind] ?? []).map(stripRuntimeFields)
  return out
}

function stripRuntimeFields(entity) {
  const out = {}
  for (const [k, v] of Object.entries(entity)) {
    if (RUNTIME_FIELDS.includes(k)) continue
    out[k] = v
  }
  return out
}
