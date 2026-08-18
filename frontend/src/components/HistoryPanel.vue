<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../api/client'
import { useGraphStore } from '../stores/graph'

const emit = defineEmits(['close'])
const graph = useGraphStore()

const entries = ref([])
const loading = ref(true)
const expanded = ref(null)

const STATUS_STYLE = {
  success: 'bg-emerald-500/15 text-emerald-300',
  partial: 'bg-amber-500/15 text-amber-300',
  failed: 'bg-rose-500/15 text-rose-300',
}

onMounted(async () => {
  try {
    entries.value = await api.history(graph.connectionId)
  } finally {
    loading.value = false
  }
})

function parsedPlan(entry) {
  try {
    return JSON.parse(entry.plan_json)
  } catch {
    return { ops: [] }
  }
}

const rows = computed(() =>
  entries.value.map((e) => ({ ...e, plan: parsedPlan(e), when: new Date(e.applied_at).toLocaleString() })),
)
</script>

<template>
  <div class="fixed inset-0 z-[65] flex justify-end bg-black/50" @click.self="emit('close')">
    <section class="flex h-full w-[520px] flex-col border-l border-[#2c3444] bg-[#141821] shadow-2xl">
      <header class="flex items-center border-b border-[#232a37] px-5 py-3">
        <div>
          <h2 class="text-base font-semibold text-slate-100">Apply history</h2>
          <p class="text-xs text-slate-500">Local audit log for this Kong.</p>
        </div>
        <button class="ml-auto text-slate-500 hover:text-slate-200" @click="emit('close')">✕</button>
      </header>

      <div v-if="loading" class="grid flex-1 place-items-center text-sm text-slate-400">Loading…</div>
      <div v-else-if="!rows.length" class="grid flex-1 place-items-center text-sm text-slate-500">
        Nothing has been applied from here yet.
      </div>

      <ul v-else class="flex-1 divide-y divide-[#1e242f] overflow-y-auto scroll-thin">
        <li v-for="row in rows" :key="row.id" class="px-5 py-3">
          <button class="flex w-full items-center gap-2 text-left" @click="expanded = expanded === row.id ? null : row.id">
            <span class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase" :class="STATUS_STYLE[row.status]">
              {{ row.status }}
            </span>
            <span class="text-sm text-slate-200">{{ row.plan.ops?.length ?? 0 }} operation(s)</span>
            <span class="ml-auto text-xs text-slate-500">{{ row.when }}</span>
          </button>
          <p v-if="row.error_message" class="mt-1 text-xs text-rose-300">{{ row.error_message }}</p>
          <ul v-if="expanded === row.id" class="mt-2 space-y-0.5 font-mono text-[11px] text-slate-400">
            <li v-for="(op, i) in row.plan.ops ?? []" :key="i">
              <span :class="op.type === 'create' ? 'text-emerald-300' : op.type === 'delete' ? 'text-rose-300' : 'text-amber-300'">
                {{ op.type }}
              </span>
              {{ op.label }}
            </li>
          </ul>
        </li>
      </ul>
    </section>
  </div>
</template>
