<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { VueFlow, useVueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import FilterBar from './FilterBar.vue'
import KongNode from './KongNode.vue'
import PluginPicker from './PluginPicker.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import { useGraphStore } from '../stores/graph'
import { deepClone } from '../api/clone'
import { KIND_META, splitNodeId } from '../api/entities'

const graph = useGraphStore()
const { project, fitView } = useVueFlow()

const nodes = computed(() => graph.nodes)
const edges = computed(() => graph.edges)

const menu = ref(null) // { x, y, kind: 'pane'|'node'|'edge', target }
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
  const labelField = KIND_META[kind].labelField
  if (copy[labelField]) copy[labelField] = `${copy[labelField]}-copy`
  graph.createEntity(kind, copy, { x: node.position.x + 40, y: node.position.y + 60 })
}

function onConnect(params) {
  const err = graph.connect(params.source, params.target)
  if (err) graph.notify(err, 'error')
}

function onNodeDragStop({ nodes: dragged }) {
  for (const node of dragged ?? []) graph.setPosition(node.id, node.position)
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

defineExpose({ relayout, focusNode })

// Re-fit whenever a different Kong is opened.
watch(
  () => graph.connectionId,
  () => nextTick(() => fitView({ padding: 0.2 })),
)
</script>

<template>
  <div class="relative h-full w-full" @click="closeMenu">
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
      @node-drag-stop="onNodeDragStop"
      @node-click="onNodeClick"
      @node-context-menu="onNodeContextMenu"
      @edge-context-menu="onEdgeContextMenu"
      @edge-double-click="({ edge }) => disconnectEdge(edge)"
      @pane-click="onPaneClick"
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
      class="fixed z-50 min-w-44 overflow-hidden rounded-md border border-[#2c3444] bg-[#171b24] py-1 text-sm shadow-xl"
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
        <button class="w-full px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]" @click="closeMenu(); relayout()">
          Auto-layout
        </button>
      </template>

      <template v-else-if="menu.kind === 'node'">
        <button
          class="w-full px-3 py-1.5 text-left text-slate-200 hover:bg-[#222835]"
          @click="graph.selectedNodeId = menu.target.id; closeMenu()"
        >
          Edit properties
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
