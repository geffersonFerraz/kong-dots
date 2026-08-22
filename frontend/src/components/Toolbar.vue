<script setup>
import { computed, ref } from 'vue'
import { useConnectionsStore } from '../stores/connections'
import { useGraphStore } from '../stores/graph'
import { useRequestsStore } from '../stores/requests'
import { useSessionStore } from '../stores/session'

const emit = defineEmits(['refresh', 'relayout', 'review', 'history', 'import', 'export', 'discard', 'requests', 'undo', 'redo'])
const connections = useConnectionsStore()
const graph = useGraphStore()
const requests = useRequestsStore()
const session = useSessionStore()

const editingIdentity = ref(false)
const draftName = ref('')
const draftToken = ref('')

function openIdentity() {
  draftName.value = session.name
  draftToken.value = session.token
  editingIdentity.value = true
}

async function saveIdentity() {
  await session.setIdentity({ name: draftName.value, token: draftToken.value })
  editingIdentity.value = false
}

// Everyone else on this Kong right now, named for the tooltip.
const othersLabel = computed(() =>
  session.others.map((p) => (p.node ? `${p.name} — editing ${p.node.split(':')[0]}` : p.name)).join('\n'),
)

const active = computed(() => connections.active)
const pending = computed(() => graph.pending)
const total = computed(() => pending.value.create + pending.value.update + pending.value.delete)

const counts = computed(() => {
  const out = {}
  for (const [kind, list] of Object.entries(graph.entities)) out[kind] = list.length
  return out
})

const btn = 'rounded-md border border-[#2c3444] px-2.5 py-1.5 text-xs text-slate-300 hover:bg-[#222835] disabled:opacity-40'
</script>

<template>
  <header class="flex items-center gap-2 border-b border-[#232a37] bg-[#141821] px-4 py-2">
    <div class="min-w-0">
      <div class="flex items-center gap-2">
        <h1 class="truncate text-sm font-semibold text-slate-100">{{ active?.name ?? 'No Kong selected' }}</h1>
        <span v-if="graph.info?.version" class="rounded bg-[#222835] px-1.5 py-0.5 text-[10px] text-slate-400">
          Kong {{ graph.info.version }} · {{ graph.info.edition }}
        </span>
      </div>
      <p class="truncate text-[11px] text-slate-500">
        {{ counts.services ?? 0 }} services · {{ counts.routes ?? 0 }} routes · {{ counts.plugins ?? 0 }} plugins ·
        {{ counts.consumers ?? 0 }} consumers · {{ counts.upstreams ?? 0 }} upstreams
      </p>
    </div>

    <span class="flex-1" />

    <span
      v-if="graph.issues.length"
      class="rounded-full bg-amber-500/20 px-2.5 py-1 text-[11px] font-medium text-amber-300"
      :title="graph.issues.map((i) => `${i.label}: ${i.message}`).join('\n')"
    >
      ⚠ {{ graph.issues.length }} to fix
    </span>

    <span
      v-if="total"
      class="rounded-full bg-amber-500/15 px-2.5 py-1 text-[11px] font-medium text-amber-300"
      :title="pending.items.map((i) => `${i.type} ${i.label}`).join('\n')"
    >
      {{ total }} unsaved change{{ total === 1 ? '' : 's' }}
    </span>

    <span
      v-if="session.others.length"
      class="rounded-full bg-sky-500/15 px-2.5 py-1 text-[11px] font-medium text-sky-300"
      :title="othersLabel"
    >
      👥 {{ session.others.length }} other{{ session.others.length === 1 ? '' : 's' }} here
    </span>

    <button
      v-if="session.approvalRequired || requests.pendingCount"
      :class="[btn, requests.pendingCount ? 'border-amber-500/40 text-amber-300' : '']"
      :disabled="!active"
      @click="emit('requests')"
    >
      Queue<span v-if="requests.pendingCount"> ({{ requests.pendingCount }})</span>
    </button>

    <button
      :class="btn"
      :disabled="!graph.canUndo"
      :title="graph.canUndo ? `Undo: ${graph.undoLabel} (Ctrl+Z)` : 'Nothing to undo'"
      @click="emit('undo')"
    >
      ↶
    </button>
    <button
      :class="btn"
      :disabled="!graph.canRedo"
      :title="graph.canRedo ? `Redo: ${graph.redoLabel} (Ctrl+Shift+Z)` : 'Nothing to redo'"
      @click="emit('redo')"
    >
      ↷
    </button>

    <button :class="btn" :disabled="!active || graph.loading" @click="emit('refresh')">
      {{ graph.loading ? 'Refreshing…' : 'Refresh' }}
    </button>
    <button
      :class="[btn, 'flex items-center gap-1.5']"
      :title="session.approver ? 'You can apply changes to Kong' : 'Your changes are filed for approval'"
      @click="openIdentity"
    >
      <span
        class="h-1.5 w-1.5 rounded-full"
        :class="session.proposesOnly ? 'bg-slate-500' : 'bg-emerald-400'"
      />
      {{ session.displayName }}
    </button>
    <button :class="btn" :disabled="!active" @click="emit('relayout')">Auto-layout</button>
    <button :class="btn" :disabled="!active" @click="emit('import')">Import decK</button>
    <button :class="btn" :disabled="!active" @click="emit('export')">Export decK</button>
    <button :class="btn" :disabled="!active" @click="emit('history')">History</button>
    <button :class="btn" :disabled="!total" @click="emit('discard')">Discard</button>
    <button
      class="rounded-md bg-sky-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-sky-500 disabled:opacity-40"
      :disabled="!active"
      @click="emit('review')"
    >
      Review changes
    </button>
  </header>

  <!-- Who this browser says it is. There is no login yet, so this is also where
       an approver hands over the token that lets them push to Kong. -->
  <div
    v-if="editingIdentity"
    class="fixed inset-0 z-[70] grid place-items-center bg-black/50"
    @click.self="editingIdentity = false"
  >
    <div class="w-[380px] rounded-lg border border-[#2c3444] bg-[#141821] p-5 shadow-2xl">
      <h2 class="text-sm font-semibold text-slate-100">Who is editing</h2>
      <p class="mt-1 text-xs text-slate-500">
        Shown to everyone else on this Kong and recorded against every change you make.
      </p>

      <label class="mt-4 block text-xs text-slate-400">Display name</label>
      <input
        v-model="draftName"
        placeholder="e.g. gefferson"
        class="mt-1 w-full rounded-md border border-[#2c3444] bg-[#10131a] px-2.5 py-1.5 text-sm text-slate-200 placeholder:text-slate-600"
        @keyup.enter="saveIdentity"
      />

      <template v-if="session.approvalRequired">
        <label class="mt-3 block text-xs text-slate-400">Approval token</label>
        <input
          v-model="draftToken"
          type="password"
          placeholder="leave empty if you are not an approver"
          class="mt-1 w-full rounded-md border border-[#2c3444] bg-[#10131a] px-2.5 py-1.5 text-sm text-slate-200 placeholder:text-slate-600"
          @keyup.enter="saveIdentity"
        />
        <p class="mt-2 text-[11px]" :class="session.approver ? 'text-emerald-300' : 'text-slate-500'">
          {{
            session.approver
              ? 'You can apply changes directly to this Kong.'
              : 'Your changes are filed for an approver to review — nothing reaches Kong until they run it.'
          }}
        </p>
      </template>

      <div class="mt-5 flex justify-end gap-2">
        <button
          class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]"
          @click="editingIdentity = false"
        >
          Cancel
        </button>
        <button class="rounded-md bg-sky-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-sky-500" @click="saveIdentity">
          Save
        </button>
      </div>
    </div>
  </div>
</template>
