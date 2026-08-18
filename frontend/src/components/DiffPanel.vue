<script setup>
import { computed } from 'vue'
import { useGraphStore } from '../stores/graph'
import { KIND_META } from '../api/entities'

const emit = defineEmits(['close', 'focus-node'])
const graph = useGraphStore()

const issues = computed(() => graph.issues)

const plan = computed(() => graph.plan)
const ops = computed(() => plan.value?.ops ?? [])

const TYPE_STYLE = {
  create: { sign: '+', text: 'text-emerald-300', chip: 'bg-emerald-500/15 text-emerald-300' },
  update: { sign: '~', text: 'text-amber-300', chip: 'bg-amber-500/15 text-amber-300' },
  delete: { sign: '-', text: 'text-rose-300', chip: 'bg-rose-500/15 text-rose-300' },
}

// Live per-operation status while an apply is running.
const opStatus = computed(() => {
  const map = {}
  for (const ev of graph.applyLog) {
    if (ev.kind === 'op_start') map[ev.index] = 'running'
    if (ev.kind === 'op_done') map[ev.index] = ev.result?.status ?? 'ok'
  }
  return map
})

const STATUS_STYLE = {
  running: 'text-sky-300',
  ok: 'text-emerald-400',
  error: 'text-rose-400',
  skipped: 'text-slate-500',
}

function short(value) {
  if (value === undefined) return '(unset)'
  if (value === null) return 'null'
  if (typeof value === 'string') return value === '' ? '""' : value
  const s = JSON.stringify(value)
  return s.length > 120 ? `${s.slice(0, 120)}…` : s
}

async function apply() {
  await graph.apply()
}
</script>

<template>
  <div class="fixed inset-0 z-[65] flex justify-end bg-black/50" @click.self="emit('close')">
    <section class="flex h-full w-[520px] flex-col border-l border-[#2c3444] bg-[#141821] shadow-2xl">
      <header class="flex items-center gap-3 border-b border-[#232a37] px-5 py-3">
        <div>
          <h2 class="text-base font-semibold text-slate-100">Review changes</h2>
          <p class="text-xs text-slate-500">Diffed against the live Admin API just now.</p>
        </div>
        <button class="ml-auto text-slate-500 hover:text-slate-200" @click="emit('close')">✕</button>
      </header>

      <div v-if="graph.planning" class="grid flex-1 place-items-center text-sm text-slate-400">Building the plan…</div>

      <div v-else-if="!ops.length" class="grid flex-1 place-items-center px-6 text-center text-sm text-slate-400">
        <div>
          <p>The canvas matches what Kong reports.</p>
          <p class="mt-1 text-slate-500">Nothing to apply.</p>
        </div>
      </div>

      <template v-else>
        <div v-if="issues.length" class="border-b border-amber-500/30 bg-amber-500/10 px-5 py-3">
          <h3 class="text-xs font-semibold uppercase tracking-wider text-amber-300">
            {{ issues.length }} problem{{ issues.length === 1 ? '' : 's' }} to fix first
          </h3>
          <ul class="mt-2 space-y-1 text-xs text-amber-100">
            <li v-for="issue in issues" :key="issue.nodeId + issue.field">
              <button class="text-left hover:underline" @click="emit('focus-node', issue.nodeId)">
                <span class="font-medium">{{ issue.label }}</span>
                <span class="text-amber-300/80"> — {{ issue.message }}</span>
              </button>
            </li>
          </ul>
          <p class="mt-2 text-[11px] text-amber-300/70">
            Kong would refuse these mid-apply, so nothing is sent until they are resolved.
          </p>
        </div>

        <div class="flex gap-2 border-b border-[#232a37] px-5 py-2 text-xs">
          <span class="rounded px-2 py-0.5" :class="TYPE_STYLE.create.chip">{{ plan.summary.create }} to create</span>
          <span class="rounded px-2 py-0.5" :class="TYPE_STYLE.update.chip">{{ plan.summary.update }} to update</span>
          <span class="rounded px-2 py-0.5" :class="TYPE_STYLE.delete.chip">{{ plan.summary.delete }} to delete</span>
        </div>

        <ul class="flex-1 divide-y divide-[#1e242f] overflow-y-auto scroll-thin">
          <li v-for="(op, i) in ops" :key="i" class="px-5 py-3">
            <div class="flex items-center gap-2">
              <span class="font-mono text-base font-bold" :class="TYPE_STYLE[op.type].text">{{ TYPE_STYLE[op.type].sign }}</span>
              <span class="h-1.5 w-1.5 rounded-full" :style="{ backgroundColor: KIND_META[op.kind]?.accent }" />
              <span class="text-sm text-slate-200">{{ op.label }}</span>
              <span v-if="opStatus[i]" class="ml-auto text-[11px] uppercase tracking-wide" :class="STATUS_STYLE[opStatus[i]]">
                {{ opStatus[i] }}
              </span>
            </div>

            <ul v-if="op.changes?.length" class="mt-2 space-y-0.5 pl-6 font-mono text-[11px]">
              <li v-for="c in op.changes" :key="c.field" class="text-slate-400">
                <span class="text-slate-300">{{ c.field }}</span>:
                <span class="text-rose-300/80">{{ short(c.from) }}</span>
                <span class="text-slate-600"> → </span>
                <span class="text-emerald-300/90">{{ short(c.to) }}</span>
              </li>
            </ul>

            <div v-else-if="op.type === 'create'" class="mt-1 pl-6 font-mono text-[11px] text-slate-500">
              {{ short(op.payload) }}
            </div>
          </li>
        </ul>

        <footer class="border-t border-[#232a37] px-5 py-3">
          <p v-if="graph.lastApply?.error" class="mb-2 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-200">
            {{ graph.lastApply.error }}
          </p>
          <div class="flex items-center gap-2">
            <button class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]" @click="graph.buildPlan()">
              Re-plan
            </button>
            <span class="flex-1" />
            <button class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]" @click="emit('close')">
              Cancel
            </button>
            <button
              class="rounded-md bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
              :disabled="graph.applying || issues.length > 0"
              :title="issues.length ? 'Fix the highlighted problems first' : ''"
              @click="apply"
            >
              {{ graph.applying ? 'Applying…' : issues.length ? 'Fix problems to apply' : `Apply ${ops.length} change(s)` }}
            </button>
          </div>
          <p class="mt-2 text-[11px] text-slate-500">
            Operations run in dependency order and stop at the first failure — there is no automatic rollback yet, and every
            run is recorded in the history.
          </p>
        </footer>
      </template>
    </section>
  </div>
</template>
