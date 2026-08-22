<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, unref, watch } from 'vue'
import { VueFlow, useVueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import FilterBar from './FilterBar.vue'
import KongNode from './KongNode.vue'
import PluginPicker from './PluginPicker.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import { useConnectionsStore } from '../stores/connections'
import { useGraphStore } from '../stores/graph'
import { useSessionStore } from '../stores/session'
import { deepClone } from '../api/clone'
import { KIND_META, routeUrls, splitNodeId } from '../api/entities'

const graph = useGraphStore()
const connections = useConnectionsStore()
const session = useSessionStore()
const { project, fitView, viewport } = useVueFlow()

// Nodes somebody else has open are marked, so two people notice before they
// both edit the same Service and one of them loses.
const nodes = computed(() => {
  const byNode = session.peersByNode
  if (!Object.keys(byNode).length) return graph.nodes
  return graph.nodes.map((n) => (byNode[n.id] ? { ...n, data: { ...n.data, peers: byNode[n.id] } } : n))
})
const edges = computed(() => graph.edges)

const menu = ref(null) // { x, y, kind: 'pane'|'node'|'edge', target }
// Where the pointer last was over the canvas, so a paste lands where the user
// is looking rather than at a fixed corner.
const lastPointer = ref(null)
const container = ref(null)
const pluginPicker = ref(null) // { position }
const confirm = ref(null)

const addableKinds = ['services', 'routes', 'plugins', 'consumers', 'upstreams', 'targets']

function closeMenu() {
  menu.value = null
}

function onPaneContextMenu(event) {
  event.preventDefault()
  menu.value = { x: event.clientX, y: event.clientY, kind: 'pane', flow: project({ x: event.offsetX, y: event.offsetY }) }
}

function onNodeContextMenu({ event, node }) {
  event.preventDefault()
  menu.value = { x: event.clientX, y: event.clientY, kind: 'node', target: node }
}

function onEdgeContextMenu({ event, edge }) {
  event.preventDefault()
  menu.value = { x: event.clientX, y: event.clientY, kind: 'edge', target: edge }
}

function addNode(kind) {
  const position = menu.value?.flow ?? { x: 80, y: 80 }
  closeMenu()
  if (kind === 'plugins') {
    pluginPicker.value = { position }
    return
  }
  graph.createEntity(kind, {}, position)
}

function addPlugin(name) {
  const position = pluginPicker.value?.position ?? { x: 80, y: 80 }
  pluginPicker.value = null
  graph.createEntity('plugins', { name }, position)
}

// URLs offered for the node under the cursor — Routes only, and only once the
// connection knows where this Kong serves traffic.
const menuUrls = computed(() => {
  const node = menu.value?.kind === 'node' ? menu.value.target : null
  if (node?.data?.kind !== 'routes') return []
  return routeUrls(node.data.entity, connections.active?.base_url)
})

const isRouteNode = computed(() => menu.value?.kind === 'node' && menu.value.target?.data?.kind === 'routes')

// writeClipboard prefers the async API and falls back to a hidden textarea,
// since the clipboard API needs a secure context and this Kong may well be
// reached over plain http on a LAN address.
async function writeClipboard(text) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text)
  const area = document.createElement('textarea')
  area.value = text
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  document.execCommand('copy')
  area.remove()
}

// copyNode puts an entity and everything belonging to it on the clipboard as
// JSON, so it can be pasted into another Kong — usually another workspace, or
// another browser window entirely.
async function copyEntity(node) {
  closeMenu()
  const { kind, id } = splitNodeId(node.id)
  try {
    const bundle = graph.clipboardBundle(kind, id)
    await writeClipboard(JSON.stringify(bundle, null, 2))
    graph.notify(`Copied ${summarise(bundle.entities)} — paste into any Kong Flow workspace`, 'success')
  } catch (e) {
    graph.notify(`Could not copy: ${e.message}`, 'error')
  }
}

// summarise reads either a map of kind -> entities or kind -> count.
function summarise(byKind) {
  const parts = []
  for (const [kind, value] of Object.entries(byKind ?? {})) {
    const n = Array.isArray(value) ? value.length : value
    if (!n) continue
    const singular = KIND_META[kind].singular.toLowerCase()
    parts.push(`${n} ${n === 1 ? singular : singular + 's'}`)
  }
  return parts.join(', ') || 'nothing'
}

// Paste rides the browser's own paste event, which hands over the clipboard
// without asking for read permission.
function onPaste(event) {
  if (['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target?.tagName)) return
  const text = event.clipboardData?.getData('text/plain')?.trim()
  if (!text || !text.startsWith('{')) return
  let bundle
  try {
    bundle = JSON.parse(text)
  } catch {
    return // some other JSON-looking text; not ours to complain about
  }
  if (bundle?.kong_flow !== 1) return
  event.preventDefault()
  try {
    const created = graph.pasteBundle(bundle, lastPointer.value ?? project({ x: 200, y: 160 }))
    graph.notify(`Pasted ${summarise(created)} — review before applying`, 'success')
  } catch (e) {
    graph.notify(`Could not paste: ${e.message}`, 'error')
  }
}

// Canvas keyboard shortcuts. Typing in a field is left alone: Ctrl+Z there
// belongs to the text box, not to the canvas.
function onCanvasKey(event) {
  const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target?.tagName) || event.target?.isContentEditable
  if (typing || !(event.ctrlKey || event.metaKey)) return
  const key = event.key.toLowerCase()

  if (key === 'z') {
    event.preventDefault()
    // Ctrl+Shift+Z redoes, matching every other editor; Ctrl+Y does too, for
    // the Windows habit.
    const action = event.shiftKey ? graph.redo() : graph.undo()
    if (action) graph.notify(`${event.shiftKey ? 'Redid' : 'Undid'}: ${action.label}`, 'info')
    else graph.notify(event.shiftKey ? 'Nothing to redo' : 'Nothing to undo', 'info')
    return
  }
  if (key === 'y') {
    event.preventDefault()
    const action = graph.redo()
    if (action) graph.notify(`Redid: ${action.label}`, 'info')
    return
  }
  if (key === 'c') {
    const selected = graph.selectedNodeId && graph.nodes.find((n) => n.id === graph.selectedNodeId)
    if (!selected) return
    event.preventDefault()
    copyEntity(selected)
  }
}

// The mousemove target is often a node, so offsetX/Y would be relative to it;
// the flow coordinate has to come from the container's own rect.
function onPaneMouseMove(event) {
  const rect = container.value?.getBoundingClientRect()
  if (!rect) return
  const flow = project({ x: event.clientX - rect.left, y: event.clientY - rect.top })
  lastPointer.value = flow
  // Flow coordinates, not screen ones: everyone is panned and zoomed
  // differently, and a pointer has to land on the same node for all of them.
  session.sendCursor(flow.x, flow.y)
}

function onPaneMouseLeave() {
  session.cursorLeft()
}

// Where each remote pointer sits on *this* screen. The viewport transform is
// what turns their flow coordinate into a pixel here.
const remoteCursors = computed(() => {
  const vp = unref(viewport) ?? { x: 0, y: 0, zoom: 1 }
  return Object.values(session.cursors).map((c) => ({
    id: c.id,
    name: c.name,
    color: peerColor(c.id),
    x: c.x * vp.zoom + vp.x,
    y: c.y * vp.zoom + vp.y,
  }))
})

// A stable colour per tab, so the same person keeps the same pointer while they
// are here. Hashing the id beats assigning by index, which would reshuffle
// every time somebody joins or leaves.
const CURSOR_COLORS = ['#38bdf8', '#f472b6', '#34d399', '#fbbf24', '#a78bfa', '#fb7185']
function peerColor(id) {
  let hash = 0
  for (let i = 0; i < String(id).length; i++) hash = (hash * 31 + String(id).charCodeAt(i)) >>> 0
  return CURSOR_COLORS[hash % CURSOR_COLORS.length]
}

async function copyUrl(url) {
  closeMenu()
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(url)
    } else {
      // The clipboard API needs a secure context; this Kong may well be reached
      // over plain http on a LAN address.
      const area = document.createElement('textarea')
      area.value = url
      area.style.position = 'fixed'
      area.style.opacity = '0'
      document.body.appendChild(area)
      area.select()
      document.execCommand('copy')
      area.remove()
    }
    graph.notify(`Copied ${url}`, 'success')
  } catch (e) {
    graph.notify(`Could not copy: ${e.message}`, 'error')
  }
}

function requestDelete(node) {
  const { kind, id } = splitNodeId(node.id)
  closeMenu()
  confirm.value = { kind, id, victims: graph.cascade(kind, id) }
}

function confirmDelete() {
  graph.deleteEntity(confirm.value.kind, confirm.value.id)
  confirm.value = null
}

function duplicate(node) {
  const { kind } = splitNodeId(node.id)
  closeMenu()
  const source = node.data.entity
  const copy = deepClone(source)
  delete copy.id
  delete copy.created_at
  delete copy.updated_at
  // Only kinds whose label is a free-form name get a "-copy" suffix. A Plugin's
  // name is the plugin type and a Target's is an address: renaming either would
  // produce something Kong has never heard of.
  const meta = KIND_META[kind]
  if (meta.renameOnDuplicate && copy[meta.labelField]) {
    copy[meta.labelField] = `${copy[meta.labelField]}-copy`
  }
  graph.createEntity(kind, copy, { x: node.position.x + 40, y: node.position.y + 60 })
}

function onConnect(params) {
  const err = graph.connect(params.source, params.target)
  if (err) graph.notify(err, 'error')
}

function onNodeDragStart({ nodes: dragged }) {
  graph.beginLocalDrag((dragged ?? []).map((n) => n.id))
}

// Streamed while the pointer is down, so the node travels on everyone's canvas
// instead of teleporting when it is let go.
function onNodeDrag({ nodes: dragged }) {
  for (const node of dragged ?? []) session.sendNodeMove(node.id, node.position.x, node.position.y)
}

function onNodeDragStop({ nodes: dragged }) {
  for (const node of dragged ?? []) {
    // Marked final: this is the frame that must not be thrown away by the
    // throttle, since it is where the node actually ended up.
    session.sendNodeMove(node.id, node.position.x, node.position.y, true)
  }
  // Recorded as one step, so Ctrl+Z puts the node back where it was.
  graph.commitDrag((dragged ?? []).map((n) => ({ node: n.id, position: n.position })))
  graph.endLocalDrag()
  // Only the person who dragged writes the layout; everyone else just follows.
  graph.persistLayout()
}

function onNodeClick({ node }) {
  graph.selectedNodeId = node.id
}

function onPaneClick() {
  closeMenu()
  graph.selectedNodeId = null
}

function disconnectEdge(edge) {
  closeMenu()
  graph.disconnect(edge)
}

// focusNode recentres the viewport on a filter hit.
function focusNode(id) {
  fitView({ nodes: [id], padding: 0.8, duration: 400, maxZoom: 1.4 })
}

function relayout() {
  graph.applyAutoLayout()
  graph.persistLayout()
  nextTick(() => fitView({ padding: 0.2 }))
}

onMounted(() => {
  window.addEventListener('paste', onPaste)
  window.addEventListener('keydown', onCanvasKey)
})
onUnmounted(() => {
  window.removeEventListener('paste', onPaste)
  window.removeEventListener('keydown', onCanvasKey)
})

defineExpose({ relayout, focusNode })

// Re-fit whenever a different Kong is opened.
watch(
  () => graph.connectionId,
  () => nextTick(() => fitView({ padding: 0.2 })),
)
</script>

<template>
  <div ref="container" class="relative h-full w-full" @click="closeMenu" @mouseleave="onPaneMouseLeave">
    <FilterBar @focus-node="focusNode" />
    <VueFlow
      :nodes="nodes"
      :edges="edges"
      :min-zoom="0.15"
      :max-zoom="2.5"
      :default-viewport="{ zoom: 0.9 }"
      :delete-key-code="null"
      fit-view-on-init
      class="h-full w-full"
      @connect="onConnect"
      @node-drag-start="onNodeDragStart"
      @node-drag="onNodeDrag"
      @node-drag-stop="onNodeDragStop"
      @node-click="onNodeClick"
      @node-context-menu="onNodeContextMenu"
      @edge-context-menu="onEdgeContextMenu"
      @edge-double-click="({ edge }) => disconnectEdge(edge)"
      @pane-click="onPaneClick"
      @mousemove="onPaneMouseMove"
      @pane-context-menu="onPaneContextMenu"
    >
      <template #node-kong="nodeProps">
        <KongNode v-bind="nodeProps" />
      </template>
      <Background pattern-color="#252c3a" :gap="22" />
      <Controls position="bottom-left" />
      <MiniMap
        pannable
        zoomable
        :node-color="(n) => KIND_META[n.data?.kind]?.accent ?? '#64748b'"
        mask-color="rgba(10,13,18,0.75)"
      />
    </VueFlow>

    <!-- Other people's pointers. Purely decorative and never interactive, so it
         must not intercept a click meant for the canvas underneath. -->
    <div class="pointer-events-none absolute inset-0 z-40 overflow-hidden">
      <div
        v-for="cursor in remoteCursors"
        :key="cursor.id"
        class="absolute left-0 top-0 will-change-transform"
        :style="{ transform: `translate(${cursor.x}px, ${cursor.y}px)` }"
      >
        <svg width="18" height="18" viewBox="0 0 18 18" class="drop-shadow">
          <path d="M2 1 L2 14 L5.5 10.8 L7.9 15.6 L10.1 14.5 L7.8 9.9 L12.4 9.6 Z"
                :fill="cursor.color" stroke="#0b0e14" stroke-width="1.1" stroke-linejoin="round" />
        </svg>
        <span
          class="ml-3 inline-block whitespace-nowrap rounded px-1.5 py-0.5 text-[10px] font-medium text-white shadow"
          :style="{ backgroundColor: cursor.color }"
        >
          {{ cursor.name }}
        </span>
      </div>
    </div>

    <div
      v-if="graph.loading"
      class="pointer-events-none absolute inset-0 grid place-items-center bg-[#10131a]/70 text-sm text-slate-300"
    >
      Reading topology from the Admin API…
    </div>

    <div
      v-else-if="!graph.nodes.length && graph.connectionId"
      class="pointer-events-none absolute inset-0 grid place-items-center"
    >
      <div class="rounded-lg border border-dashed border-[#2c3444] px-6 py-4 text-center text-sm text-slate-400">
        <p>This Kong has no entities yet.</p>
        <p class="mt-1 text-slate-500">Right-click the canvas to add a Service.</p>
      </div>
    </div>

    <!-- Context menu -->
    <div
      v-if="menu"
      class="fixed z-50 min-w-44 max-w-[340px] overflow-hidden rounded-md border border-[#2c3444] bg-[#171b24] py-1 text-sm shadow-xl"
      :style="{ left: menu.x + 'px', top: menu.y + 'px' }"
      @click.stop
    >
      <template v-if="menu.kind === 'pane'">
        <div class="px-3 py-1 text-[10px] uppercase tracking-wider text-slate-500">Add node</div>
        <button
          v-for="kind in addableKinds"
          :key="kind"
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]"
          @click="addNode(kind)"
        >
          <span class="h-2 w-2 rounded-full" :style="{ backgroundColor: KIND_META[kind].accent }" />
          {{ KIND_META[kind].singular }}
        </button>
        <div class="my-1 border-t border-[#2c3444]" />
        <div class="flex items-center gap-2 px-3 py-1.5 text-slate-400">
          Paste
          <span class="ml-auto text-[10px] text-slate-500">⌘/Ctrl+V</span>
        </div>
        <button class="w-full px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]" @click="closeMenu(); relayout()">
          Auto-layout
        </button>
      </template>

      <template v-else-if="menu.kind === 'node'">
        <template v-if="menuUrls.length">
          <div class="px-3 py-1 text-[10px] uppercase tracking-wider text-slate-500">
            Copy URL
          </div>
          <button
            v-for="url in menuUrls"
            :key="url"
            class="block w-full truncate px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]"
            :title="url"
            @click="copyUrl(url)"
          >
            {{ url }}
          </button>
          <div class="my-1 border-t border-[#2c3444]" />
        </template>
        <div v-else-if="isRouteNode" class="px-3 py-1.5 text-[11px] leading-snug text-slate-500">
          Set a proxy base URL on this Kong to copy Route URLs.
        </div>

        <button
          class="w-full px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]"
          @click="graph.selectedNodeId = menu.target.id; closeMenu()"
        >
          Edit properties
        </button>
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]"
          @click="copyEntity(menu.target)"
        >
          Copy
          <span class="ml-auto text-[10px] text-slate-500">⌘/Ctrl+C</span>
        </button>
        <button class="w-full px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]" @click="duplicate(menu.target)">
          Duplicate
        </button>
        <div class="my-1 border-t border-[#2c3444]" />
        <button class="w-full px-3 py-1.5 text-left text-rose-300 hover:bg-rose-500/10" @click="requestDelete(menu.target)">
          Delete…
        </button>
      </template>

      <template v-else>
        <button class="w-full px-3 py-1.5 text-left text-rose-300 hover:bg-rose-500/10" @click="disconnectEdge(menu.target)">
          Disconnect
        </button>
      </template>
    </div>

    <PluginPicker v-if="pluginPicker" :plugins="graph.availablePlugins" @pick="addPlugin" @close="pluginPicker = null" />

    <ConfirmDialog
      v-if="confirm"
      title="Delete from the canvas?"
      confirm-label="Remove"
      danger
      @cancel="confirm = null"
      @confirm="confirmDelete"
    >
      <p class="text-sm text-slate-300">
        These {{ confirm.victims.length }} entit{{ confirm.victims.length === 1 ? 'y' : 'ies' }} will be removed. Nothing
        is sent to Kong until you apply.
      </p>
      <ul class="mt-3 max-h-52 space-y-1 overflow-auto text-sm scroll-thin">
        <li v-for="v in confirm.victims" :key="v.kind + v.id" class="flex items-center gap-2 text-slate-300">
          <span class="h-1.5 w-1.5 rounded-full" :style="{ backgroundColor: KIND_META[v.kind].accent }" />
          <span class="text-slate-500">{{ KIND_META[v.kind].singular }}</span>
          {{ v.label }}
        </li>
      </ul>
    </ConfirmDialog>
  </div>
</template>
