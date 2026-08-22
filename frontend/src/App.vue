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
import RequestsPanel from './components/RequestsPanel.vue'
import { useConnectionsStore } from './stores/connections'
import { useGraphStore } from './stores/graph'
import { useRequestsStore } from './stores/requests'
import { useSessionStore } from './stores/session'
import { openSocket } from './api/client'

const connections = useConnectionsStore()
const graph = useGraphStore()
const requests = useRequestsStore()
const session = useSessionStore()

const canvas = ref(null)
const connectionDialog = ref(null) // { connection } | null
const showDiff = ref(false)
const showHistory = ref(false)
const showImport = ref(false)
const showRequests = ref(false)
const confirmDiscard = ref(false)
const toast = ref(null)

let socket = null
let toastTimer = null
// Whether this tab has already asked the others for the shared draft. Once per
// Kong: asking again would pull back a draft the user has deliberately left.
let askedForCanvas = false

onMounted(async () => {
  await session.init()
  connections.load()
})

// Keep one socket open for the Kong currently on screen: apply progress, who
// else is here, and a nudge when somebody's change lands.
watch(
  () => connections.activeId,
  (id) => {
    socket?.close()
    socket = null
    session.detach()
    askedForCanvas = false
    if (!id) {
      graph.reset()
      requests.reset()
      showRequests.value = false
      return
    }
    graph.load(id)
    requests.load(id)
    socket = openSocket(id, { clientId: session.clientId, name: session.displayName }, onServerEvent)
    socket.onopen = () => session.announce(graph.selectedNodeId)
    session.attach(socket)
  },
  { immediate: true },
)

function onServerEvent(msg) {
  switch (msg.type) {
    case 'apply_started':
      graph.applyLog = []
      break
    case 'apply_progress':
      graph.recordApplyEvent(msg.payload)
      break
    case 'presence': {
      session.setPeers(msg.payload?.peers)
      // The first roster that shows somebody else already here is the moment to
      // ask for the draft they have been building.
      if (!askedForCanvas && session.others.length) askedForCanvas = session.requestCanvas()
      break
    }
    case 'cursor':
      session.applyCursor(msg.payload)
      break
    case 'node_move':
      graph.applyRemotePosition(msg.payload)
      break
    case 'canvas_op':
      graph.applyRemoteCanvasOp(msg.payload)
      break
    case 'state_request':
      // Only the longest-serving tab answers, so a newcomer gets one copy.
      if (session.answersCanvasRequests) session.sendCanvas(graph.canvasSnapshot())
      break
    case 'state_sync':
      graph.applyStateSync(msg.payload)
      break
    case 'state_changed':
      graph.noteRemoteChange(msg.payload)
      break
    case 'request_submitted':
    case 'request_reviewed':
      requests.upsert(msg.payload?.request)
      if (msg.payload?.by !== session.clientId) {
        graph.notify(
          msg.type === 'request_submitted'
            ? `${msg.payload?.actor ?? 'Somebody'} filed a change for approval`
            : `${msg.payload?.actor ?? 'Somebody'} ${msg.payload?.request?.status ?? 'reviewed'} a change request`,
          'info',
        )
      }
      break
  }
}

// Tell everyone else which node this browser has open, so two people do not
// silently edit the same Service.
watch(
  () => graph.selectedNodeId,
  (node) => session.announce(node),
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
  session.detach()
  clearTimeout(toastTimer)
})

// refreshAfterRemoteChange takes the other person's version. Anything unapplied
// on this canvas is a change the user still has, so it is worth saying so.
async function refreshAfterRemoteChange() {
  const dirty = graph.isDirty
  graph.dismissRemoteChange()
  if (dirty && !confirm('You have unapplied changes on this canvas. Reload from Kong and lose them?')) return
  await graph.load(connections.activeId, { keepPositions: true })
}

function selectConnection(id) {
  if (id === connections.activeId) return
  if (graph.isDirty && !confirm('This workspace has unapplied changes. Switch anyway and lose them?')) return
  connections.select(id)
}

// Refresh is a deliberate "show me what Kong says". Since the draft is shared,
// that has to reach the others too, or this tab would sit alone on Kong's state
// while everyone else keeps the draft it just dropped.
async function refresh() {
  await graph.refresh()
  session.sendCanvas(graph.canvasSnapshot())
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
        @refresh="refresh"
        @undo="graph.undo()"
        @redo="graph.redo()"
        @relayout="canvas?.relayout()"
        @review="review"
        @history="showHistory = true"
        @import="showImport = true"
        @export="exportDeck"
        @discard="confirmDiscard = true"
        @requests="showRequests = true"
      />

      <!-- Somebody else's change reached this Kong; this canvas is now stale. -->
      <div
        v-if="graph.remoteChange"
        class="flex items-center gap-3 border-b border-sky-500/30 bg-sky-500/10 px-4 py-1.5 text-xs text-sky-200"
      >
        <span>
          {{ graph.remoteChange.actor }} applied changes to this Kong — what you are looking at is out of date.
        </span>
        <button class="rounded border border-sky-400/40 px-2 py-0.5 hover:bg-sky-500/20" @click="refreshAfterRemoteChange">
          Refresh
        </button>
        <button class="ml-auto text-sky-300/70 hover:text-sky-100" @click="graph.dismissRemoteChange()">✕</button>
      </div>

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
    <RequestsPanel v-if="showRequests" @close="showRequests = false" />
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
