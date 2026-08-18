<script setup>
import { onMounted, onUnmounted, ref, watch } from 'vue'
import WorkspaceSidebar from './components/WorkspaceSidebar.vue'
import Toolbar from './components/Toolbar.vue'
import GraphCanvas from './components/GraphCanvas.vue'
import PropertiesPanel from './components/PropertiesPanel.vue'
import ConnectionDialog from './components/ConnectionDialog.vue'
import DiffPanel from './components/DiffPanel.vue'
import HistoryPanel from './components/HistoryPanel.vue'
import ImportDialog from './components/ImportDialog.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import { useConnectionsStore } from './stores/connections'
import { useGraphStore } from './stores/graph'
import { openSocket } from './api/client'

const connections = useConnectionsStore()
const graph = useGraphStore()

const canvas = ref(null)
const connectionDialog = ref(null) // { connection } | null
const showDiff = ref(false)
const showHistory = ref(false)
const showImport = ref(false)
const confirmDiscard = ref(false)
const toast = ref(null)

let socket = null
let toastTimer = null

onMounted(() => connections.load())

// Keep one socket open for the Kong currently on screen, for apply progress.
watch(
  () => connections.activeId,
  (id) => {
    socket?.close()
    socket = null
    if (!id) {
      graph.reset()
      return
    }
    graph.load(id)
    socket = openSocket(id, (msg) => {
      if (msg.type === 'apply_progress') graph.recordApplyEvent(msg.payload)
      if (msg.type === 'apply_started') graph.applyLog = []
    })
  },
  { immediate: true },
)

watch(
  () => graph.toast,
  (t) => {
    if (!t) return
    toast.value = t
    clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toast.value = null), 5000)
  },
)

onUnmounted(() => {
  socket?.close()
  clearTimeout(toastTimer)
})

function selectConnection(id) {
  if (id === connections.activeId) return
  if (graph.isDirty && !confirm('This workspace has unapplied changes. Switch anyway and lose them?')) return
  connections.select(id)
}

async function review() {
  showDiff.value = true
  await graph.buildPlan()
}

// Jumping from a validation problem to the node that has it.
function focusFromDiff(nodeId) {
  showDiff.value = false
  graph.selectedNodeId = nodeId
  canvas.value?.focusNode(nodeId)
}

async function exportDeck() {
  try {
    const yaml = await graph.exportDeck()
    const name = (connections.active?.name ?? 'kong').toLowerCase().replace(/\s+/g, '-')
    const url = URL.createObjectURL(new Blob([yaml], { type: 'application/yaml' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `${name}.kong.yaml`
    a.click()
    URL.revokeObjectURL(url)
    graph.notify('Exported the live state as kong.yaml', 'success')
  } catch (e) {
    graph.notify(`Export failed: ${e.message}`, 'error')
  }
}

async function importDeck(yaml) {
  try {
    await graph.importDeck(yaml)
    showImport.value = false
  } catch (e) {
    graph.notify(`Import failed: ${e.message}`, 'error')
  }
}

function onConnectionSaved() {
  connectionDialog.value = null
  if (connections.activeId) graph.load(connections.activeId, { keepPositions: true })
}

const TOAST_STYLE = {
  success: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-200',
  error: 'border-rose-500/40 bg-rose-500/10 text-rose-200',
  info: 'border-sky-500/40 bg-sky-500/10 text-sky-200',
}
</script>

<template>
  <div class="flex h-full w-full overflow-hidden">
    <WorkspaceSidebar
      @select="selectConnection"
      @create="connectionDialog = { connection: null }"
      @edit="(c) => (connectionDialog = { connection: c })"
    />

    <main class="flex min-w-0 flex-1 flex-col">
      <Toolbar
        @refresh="graph.refresh()"
        @relayout="canvas?.relayout()"
        @review="review"
        @history="showHistory = true"
        @import="showImport = true"
        @export="exportDeck"
        @discard="confirmDiscard = true"
      />

      <div class="flex min-h-0 flex-1">
        <div class="relative min-w-0 flex-1">
          <div v-if="!connections.activeId" class="grid h-full place-items-center px-6 text-center">
            <div>
              <p class="text-sm text-slate-400">No Kong registered yet.</p>
              <button
                class="mt-3 rounded-md bg-sky-600 px-4 py-2 text-sm font-medium text-white hover:bg-sky-500"
                @click="connectionDialog = { connection: null }"
              >
                Register a Kong
              </button>
            </div>
          </div>
          <GraphCanvas v-else ref="canvas" />

          <p
            v-if="graph.error"
            class="absolute inset-x-6 top-4 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-200"
          >
            {{ graph.error }}
          </p>
        </div>

        <PropertiesPanel v-if="connections.activeId" />
      </div>
    </main>

    <ConnectionDialog
      v-if="connectionDialog"
      :connection="connectionDialog.connection"
      @close="connectionDialog = null"
      @saved="onConnectionSaved"
      @deleted="connectionDialog = null"
    />
    <DiffPanel v-if="showDiff" @close="showDiff = false" @focus-node="focusFromDiff" />
    <HistoryPanel v-if="showHistory" @close="showHistory = false" />
    <ImportDialog v-if="showImport" @close="showImport = false" @import="importDeck" />

    <ConfirmDialog
      v-if="confirmDiscard"
      title="Discard canvas changes?"
      confirm-label="Discard"
      danger
      @cancel="confirmDiscard = false"
      @confirm="graph.discardChanges(); confirmDiscard = false"
    >
      <p class="text-sm text-slate-300">
        The canvas goes back to what Kong reported at
        {{ graph.fetchedAt ? new Date(graph.fetchedAt).toLocaleTimeString() : 'the last refresh' }}.
      </p>
    </ConfirmDialog>

    <Transition name="fade">
      <div
        v-if="toast"
        class="fixed bottom-5 left-1/2 z-[80] -translate-x-1/2 rounded-md border px-4 py-2 text-sm shadow-xl"
        :class="TOAST_STYLE[toast.kind] ?? TOAST_STYLE.info"
      >
        {{ toast.message }}
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
