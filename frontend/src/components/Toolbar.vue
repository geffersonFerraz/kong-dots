<script setup>
import { computed } from 'vue'
import { useConnectionsStore } from '../stores/connections'
import { useGraphStore } from '../stores/graph'

const emit = defineEmits(['refresh', 'relayout', 'review', 'history', 'import', 'export', 'discard'])
const connections = useConnectionsStore()
const graph = useGraphStore()

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

    <button :class="btn" :disabled="!active || graph.loading" @click="emit('refresh')">
      {{ graph.loading ? 'Refreshing…' : 'Refresh' }}
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
</template>
