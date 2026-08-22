<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../api/client'
import { useGraphStore } from '../stores/graph'
import { useSessionStore } from '../stores/session'

const emit = defineEmits(['close'])
const graph = useGraphStore()
const session = useSessionStore()

const entries = ref([])
const loading = ref(true)
const expanded = ref(null)

// The run being undone: its rebuilt plan, and whatever went wrong trying.
const reverting = ref(null) // { entry, plan, busy, error, force }

const STATUS_STYLE = {
  success: 'bg-emerald-500/15 text-emerald-300',
  partial: 'bg-amber-500/15 text-amber-300',
  failed: 'bg-rose-500/15 text-rose-300',
}

const TYPE_COLOR = {
  create: 'text-emerald-300',
  update: 'text-amber-300',
  delete: 'text-rose-300',
}

onMounted(load)

async function load() {
  loading.value = true
  try {
    entries.value = await api.history(graph.connectionId)
  } finally {
    loading.value = false
  }
}

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

// A run that changed nothing has nothing to undo.
const revertible = (row) => row.status !== 'failed' && (row.plan.ops?.length ?? 0) > 0

async function preview(row) {
  reverting.value = { entry: row, plan: null, busy: true, error: null, force: false }
  try {
    const res = await api.rollbackPreview(graph.connectionId, row.id)
    reverting.value.plan = res.plan
  } catch (e) {
    reverting.value.error = e.message
  } finally {
    reverting.value.busy = false
  }
}

async function confirmRevert() {
  const state = reverting.value
  state.busy = true
  state.error = null
  try {
    const res = await api.rollback(graph.connectionId, state.entry.id, { force: state.force, client_id: session.clientId })
    const undone = res.plan?.ops?.length ?? 0
    graph.notify(undone ? `Rolled back ${undone} change(s)` : (res.note ?? 'Nothing to roll back'), 'success')
    reverting.value = null
    await Promise.all([load(), graph.load(graph.connectionId, { keepPositions: true })])
  } catch (e) {
    state.error = e.message
    // A refusal comes back with the plan it would not run, drift included.
    if (e.status === 409 && e.body?.plan) state.plan = e.body.plan
    state.busy = false
  }
}

function short(value) {
  if (value === undefined) return '(unset)'
  if (value === null) return 'null'
  if (typeof value === 'string') return value === '' ? '""' : value
  const s = JSON.stringify(value)
  return s.length > 90 ? `${s.slice(0, 90)}…` : s
}

const conflicts = computed(() => reverting.value?.plan?.conflicts ?? [])
const revertOps = computed(() => reverting.value?.plan?.ops ?? [])
</script>

<template>
  <div class="fixed inset-0 z-[65] flex justify-end bg-black/50" @click.self="emit('close')">
    <section class="flex h-full w-[560px] flex-col border-l border-[#2c3444] bg-[#141821] shadow-2xl">
      <header class="flex items-center border-b border-[#232a37] px-5 py-3">
        <div>
          <h2 class="text-base font-semibold text-slate-100">Apply history</h2>
          <p class="text-xs text-slate-500">Every run against this Kong, newest first.</p>
        </div>
        <button class="ml-auto text-slate-500 hover:text-slate-200" @click="emit('close')">✕</button>
      </header>

      <div v-if="loading" class="grid flex-1 place-items-center text-sm text-slate-400">Loading…</div>
      <div v-else-if="!rows.length" class="grid flex-1 place-items-center text-sm text-slate-500">
        Nothing has been applied from here yet.
      </div>

      <ul v-else class="flex-1 divide-y divide-[#1e242f] overflow-y-auto scroll-thin">
        <li v-for="row in rows" :key="row.id" class="px-5 py-3">
          <div class="flex items-center gap-2">
            <button class="flex min-w-0 flex-1 items-center gap-2 text-left" @click="expanded = expanded === row.id ? null : row.id">
              <span class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase" :class="STATUS_STYLE[row.status]">
                {{ row.status }}
              </span>
              <span class="text-sm text-slate-200">{{ row.plan.ops?.length ?? 0 }} operation(s)</span>
              <span class="ml-auto shrink-0 text-xs text-slate-500">{{ row.when }}</span>
            </button>
            <button
              v-if="revertible(row) && session.approver"
              class="shrink-0 rounded border border-[#2c3444] px-2 py-0.5 text-[11px] text-slate-300 hover:bg-[#222835]"
              title="Undo this run against Kong"
              @click="preview(row)"
            >
              Revert…
            </button>
          </div>
          <p class="mt-1 truncate text-[11px] text-slate-500">{{ row.actor }}</p>
          <p v-if="row.error_message" class="mt-1 text-xs text-rose-300">{{ row.error_message }}</p>
          <ul v-if="expanded === row.id" class="mt-2 space-y-0.5 font-mono text-[11px] text-slate-400">
            <li v-for="(op, i) in row.plan.ops ?? []" :key="i">
              <span :class="TYPE_COLOR[op.type]">{{ op.type }}</span>
              {{ op.label }}
            </li>
          </ul>
        </li>
      </ul>
    </section>

    <!-- What undoing that run would do to Kong, rebuilt against it just now. -->
    <div v-if="reverting" class="fixed inset-0 z-[75] grid place-items-center bg-black/60" @click.self="reverting = null">
      <div class="flex max-h-[80vh] w-[560px] flex-col rounded-lg border border-[#2c3444] bg-[#141821] shadow-2xl">
        <header class="border-b border-[#232a37] px-5 py-3">
          <h2 class="text-sm font-semibold text-slate-100">Roll this run back?</h2>
          <p class="mt-1 text-xs text-slate-500">
            Applied {{ new Date(reverting.entry.applied_at).toLocaleString() }} by {{ reverting.entry.actor }}.
            The plan below was rebuilt against Kong just now, so anything already undone by hand is left out.
          </p>
        </header>

        <div v-if="reverting.busy && !reverting.plan" class="grid flex-1 place-items-center px-5 py-8 text-sm text-slate-400">
          Working out what to undo…
        </div>

        <template v-else>
          <div v-if="conflicts.length" class="border-b border-rose-500/30 bg-rose-500/10 px-5 py-3">
            <h3 class="text-xs font-semibold uppercase tracking-wider text-rose-300">
              {{ conflicts.length }} conflict{{ conflicts.length === 1 ? '' : 's' }}
            </h3>
            <ul class="mt-2 space-y-1.5 text-xs text-rose-100">
              <li v-for="c in conflicts" :key="c.kind + c.entity_id">
                <span class="font-medium">{{ c.label }}</span>
                <span class="text-rose-300/80">
                  — {{ c.reason === 'deleted' ? 'no longer in Kong' : 'changed after that run' }}
                </span>
                <ul v-if="c.changes?.length" class="mt-0.5 pl-3 font-mono text-[11px] text-rose-200/70">
                  <li v-for="ch in c.changes" :key="ch.field">{{ ch.field }}: {{ short(ch.from) }} → {{ short(ch.to) }}</li>
                </ul>
              </li>
            </ul>
            <p class="mt-2 text-[11px] text-rose-300/70">
              Rolling back anyway replaces that newer work with what was there before the run.
            </p>
          </div>

          <ul v-if="revertOps.length" class="min-h-0 flex-1 divide-y divide-[#1e242f] overflow-y-auto scroll-thin">
            <li v-for="(op, i) in revertOps" :key="i" class="px-5 py-2">
              <div class="flex items-center gap-2 text-sm">
                <span class="font-mono font-bold" :class="TYPE_COLOR[op.type]">
                  {{ op.type === 'create' ? '+' : op.type === 'delete' ? '-' : '~' }}
                </span>
                <span class="text-slate-200">{{ op.label }}</span>
              </div>
              <ul v-if="op.changes?.length" class="mt-1 space-y-0.5 pl-5 font-mono text-[11px] text-slate-400">
                <li v-for="c in op.changes" :key="c.field">
                  {{ c.field }}: <span class="text-rose-300/80">{{ short(c.from) }}</span>
                  <span class="text-slate-600"> → </span><span class="text-emerald-300/90">{{ short(c.to) }}</span>
                </li>
              </ul>
            </li>
          </ul>
          <p v-else class="px-5 py-6 text-center text-sm text-slate-400">
            Kong already looks the way it did before that run — there is nothing to undo.
          </p>
        </template>

        <footer class="border-t border-[#232a37] px-5 py-3">
          <p v-if="reverting.error" class="mb-2 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-200">
            {{ reverting.error }}
          </p>
          <div class="flex items-center gap-2">
            <span class="flex-1" />
            <button
              class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]"
              @click="reverting = null"
            >
              Cancel
            </button>
            <button
              v-if="!conflicts.length"
              class="rounded-md bg-rose-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-rose-500 disabled:opacity-50"
              :disabled="reverting.busy || !revertOps.length"
              @click="confirmRevert"
            >
              {{ reverting.busy ? 'Rolling back…' : `Roll back ${revertOps.length} change(s)` }}
            </button>
            <button
              v-else-if="!reverting.force"
              class="rounded-md border border-amber-500/50 px-3 py-1.5 text-sm text-amber-300 hover:bg-amber-500/10"
              @click="reverting.force = true"
            >
              Roll back despite conflicts…
            </button>
            <button
              v-else
              class="rounded-md bg-rose-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-rose-500 disabled:opacity-50"
              :disabled="reverting.busy"
              @click="confirmRevert"
            >
              {{ reverting.busy ? 'Rolling back…' : 'Overwrite and roll back' }}
            </button>
          </div>
        </footer>
      </div>
    </div>
  </div>
</template>
