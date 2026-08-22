<script setup>
import { computed, onMounted, ref } from 'vue'
import { KIND_META } from '../api/entities'
import { useRequestsStore } from '../stores/requests'
import { useSessionStore } from '../stores/session'
import { useGraphStore } from '../stores/graph'

const emit = defineEmits(['close'])
const requests = useRequestsStore()
const session = useSessionStore()
const graph = useGraphStore()

const note = ref('')
const confirmForce = ref(false)

onMounted(() => requests.load(graph.connectionId))

const STATUS_STYLE = {
  pending: 'bg-amber-500/15 text-amber-300',
  applied: 'bg-emerald-500/15 text-emerald-300',
  rejected: 'bg-rose-500/15 text-rose-300',
  failed: 'bg-rose-500/15 text-rose-300',
  withdrawn: 'bg-slate-600/30 text-slate-400',
}

const TYPE_STYLE = {
  create: { sign: '+', text: 'text-emerald-300' },
  update: { sign: '~', text: 'text-amber-300' },
  delete: { sign: '-', text: 'text-rose-300' },
}

const open = computed(() => requests.detail?.request ?? null)
const plan = computed(() => requests.plan)
const ops = computed(() => plan.value?.ops ?? [])
const conflicts = computed(() => requests.conflicts)
const ignored = computed(() => plan.value?.ignored ?? [])

// Only the person who filed a request can take it back; an approver can clear
// anything out of the queue.
const canWithdraw = computed(
  () => open.value?.status === 'pending' && (session.approver || open.value.requested_by === session.displayName),
)
const canDecide = computed(() => open.value?.status === 'pending' && session.approver)

function summarise(request) {
  const s = request.summary
  if (!s) return 'no recorded plan'
  const parts = []
  if (s.create) parts.push(`${s.create} to create`)
  if (s.update) parts.push(`${s.update} to update`)
  if (s.delete) parts.push(`${s.delete} to delete`)
  return parts.join(', ') || 'nothing to do'
}

const when = (iso) => (iso ? new Date(iso).toLocaleString() : '')

async function decide(verdict, force = false) {
  const res = await requests.decide(verdict, open.value.id, { note: note.value, force })
  if (!res) return
  note.value = ''
  confirmForce.value = false
  await requests.load(graph.connectionId)
  if (verdict === 'approve') {
    graph.notify(`Applied ${ops.value.length} change(s) to Kong`, 'success')
    await graph.load(graph.connectionId, { keepPositions: true })
  }
}

function short(value) {
  if (value === undefined) return '(unset)'
  if (value === null) return 'null'
  if (typeof value === 'string') return value === '' ? '""' : value
  const s = JSON.stringify(value)
  return s.length > 90 ? `${s.slice(0, 90)}…` : s
}
</script>

<template>
  <div class="fixed inset-0 z-[65] flex justify-end bg-black/50" @click.self="emit('close')">
    <section class="flex h-full w-[560px] flex-col border-l border-[#2c3444] bg-[#141821] shadow-2xl">
      <header class="flex items-center gap-3 border-b border-[#232a37] px-5 py-3">
        <div class="min-w-0">
          <h2 class="text-base font-semibold text-slate-100">
            {{ open ? 'Review change' : 'Changes waiting for approval' }}
          </h2>
          <p class="truncate text-xs text-slate-500">
            <template v-if="open">
              Filed by {{ open.requested_by }} · {{ when(open.requested_at) }}
            </template>
            <template v-else>Nothing here has touched Kong yet.</template>
          </p>
        </div>
        <button v-if="open" class="ml-auto text-xs text-slate-400 hover:text-slate-200" @click="requests.close()">
          ← Back
        </button>
        <button class="text-slate-500 hover:text-slate-200" :class="open ? '' : 'ml-auto'" @click="emit('close')">✕</button>
      </header>

      <p v-if="requests.error" class="border-b border-rose-500/30 bg-rose-500/10 px-5 py-2 text-xs text-rose-200">
        {{ requests.error }}
      </p>

      <!-- Queue -->
      <template v-if="!open">
        <div v-if="requests.loading" class="grid flex-1 place-items-center text-sm text-slate-400">Loading…</div>
        <div v-else-if="!requests.list.length" class="grid flex-1 place-items-center px-6 text-center text-sm text-slate-500">
          <div>
            <p>No change requests for this Kong.</p>
            <p v-if="session.proposesOnly" class="mt-1 text-slate-600">
              Your changes land here when you press Apply.
            </p>
          </div>
        </div>

        <ul v-else class="flex-1 divide-y divide-[#1e242f] overflow-y-auto scroll-thin">
          <li v-for="r in requests.list" :key="r.id">
            <button class="w-full px-5 py-3 text-left hover:bg-[#181d27]" @click="requests.open(r.id)">
              <div class="flex items-center gap-2">
                <span class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase" :class="STATUS_STYLE[r.status]">
                  {{ r.status }}
                </span>
                <span class="truncate text-sm text-slate-200">{{ r.title || 'Untitled change' }}</span>
                <span class="ml-auto shrink-0 text-xs text-slate-500">{{ when(r.requested_at) }}</span>
              </div>
              <p class="mt-1 text-xs text-slate-500">
                {{ r.requested_by }} · {{ summarise(r) }}
                <template v-if="r.reviewed_by"> · {{ r.status }} by {{ r.reviewed_by }}</template>
              </p>
              <p v-if="r.review_note" class="mt-1 text-xs text-slate-400 italic">“{{ r.review_note }}”</p>
            </button>
          </li>
        </ul>
      </template>

      <!-- One request -->
      <template v-else>
        <div v-if="requests.busy && !plan" class="grid flex-1 place-items-center text-sm text-slate-400">
          Re-planning against Kong…
        </div>

        <div v-else class="flex min-h-0 flex-1 flex-col">
          <div class="border-b border-[#232a37] px-5 py-3">
            <p class="text-sm text-slate-200">{{ open.title || 'Untitled change' }}</p>
            <p class="mt-1 text-xs text-slate-500">
              The plan below was rebuilt against Kong just now, not when the request was written.
            </p>
          </div>

          <div v-if="conflicts.length" class="border-b border-rose-500/30 bg-rose-500/10 px-5 py-3">
            <h3 class="text-xs font-semibold uppercase tracking-wider text-rose-300">
              {{ conflicts.length }} conflict{{ conflicts.length === 1 ? '' : 's' }}
            </h3>
            <ul class="mt-2 space-y-1.5 text-xs text-rose-100">
              <li v-for="c in conflicts" :key="c.kind + c.entity_id">
                <span class="font-medium">{{ c.label }}</span>
                <span class="text-rose-300/80">
                  — {{ c.reason === 'deleted' ? 'deleted from Kong since this was written' : 'changed in Kong since this was written' }}
                </span>
                <ul v-if="c.changes?.length" class="mt-0.5 pl-3 font-mono text-[11px] text-rose-200/70">
                  <li v-for="ch in c.changes" :key="ch.field">
                    {{ ch.field }}: {{ short(ch.from) }} → {{ short(ch.to) }}
                  </li>
                </ul>
              </li>
            </ul>
            <p class="mt-2 text-[11px] text-rose-300/70">
              Approving anyway replaces that work with what this request says.
            </p>
          </div>

          <div v-if="ignored.length" class="border-b border-[#232a37] px-5 py-2 text-[11px] text-slate-500">
            {{ ignored.length }} entit{{ ignored.length === 1 ? 'y' : 'ies' }} created after this request was written
            {{ ignored.length === 1 ? 'is' : 'are' }} left untouched:
            {{ ignored.map((i) => i.label).join(', ') }}
          </div>

          <ul v-if="ops.length" class="min-h-0 flex-1 divide-y divide-[#1e242f] overflow-y-auto scroll-thin">
            <li v-for="(op, i) in ops" :key="i" class="px-5 py-2.5">
              <div class="flex items-center gap-2">
                <span class="font-mono text-base font-bold" :class="TYPE_STYLE[op.type].text">
                  {{ TYPE_STYLE[op.type].sign }}
                </span>
                <span class="h-1.5 w-1.5 rounded-full" :style="{ backgroundColor: KIND_META[op.kind]?.accent }" />
                <span class="text-sm text-slate-200">{{ op.label }}</span>
              </div>
              <ul v-if="op.changes?.length" class="mt-1 space-y-0.5 pl-6 font-mono text-[11px] text-slate-400">
                <li v-for="c in op.changes" :key="c.field">
                  <span class="text-slate-300">{{ c.field }}</span>:
                  <span class="text-rose-300/80">{{ short(c.from) }}</span>
                  <span class="text-slate-600"> → </span>
                  <span class="text-emerald-300/90">{{ short(c.to) }}</span>
                </li>
              </ul>
            </li>
          </ul>

          <div v-else class="grid flex-1 place-items-center px-6 text-center text-sm text-slate-400">
            <div>
              <p v-if="open.status === 'pending'">Kong already matches this request.</p>
              <p v-else>This request was {{ open.status }} by {{ open.reviewed_by || 'somebody' }}.</p>
              <p v-if="open.review_note" class="mt-1 text-slate-500 italic">“{{ open.review_note }}”</p>
            </div>
          </div>

          <footer v-if="canDecide || canWithdraw" class="border-t border-[#232a37] px-5 py-3">
            <input
              v-model="note"
              placeholder="Note for the author (optional)"
              class="mb-2 w-full rounded-md border border-[#2c3444] bg-[#10131a] px-2.5 py-1.5 text-sm text-slate-200 placeholder:text-slate-600"
            />
            <div class="flex items-center gap-2">
              <button
                v-if="canWithdraw"
                class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]"
                :disabled="requests.busy"
                @click="decide('withdraw')"
              >
                Withdraw
              </button>
              <span class="flex-1" />
              <template v-if="canDecide">
                <button
                  class="rounded-md border border-rose-500/40 px-3 py-1.5 text-sm text-rose-300 hover:bg-rose-500/10"
                  :disabled="requests.busy"
                  @click="decide('reject')"
                >
                  Reject
                </button>
                <button
                  v-if="!conflicts.length"
                  class="rounded-md bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
                  :disabled="requests.busy || !ops.length"
                  @click="decide('approve')"
                >
                  {{ requests.busy ? 'Applying…' : `Approve and apply ${ops.length} change(s)` }}
                </button>
                <button
                  v-else-if="!confirmForce"
                  class="rounded-md border border-amber-500/50 px-3 py-1.5 text-sm text-amber-300 hover:bg-amber-500/10"
                  @click="confirmForce = true"
                >
                  Approve despite conflicts…
                </button>
                <button
                  v-else
                  class="rounded-md bg-rose-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-rose-500 disabled:opacity-50"
                  :disabled="requests.busy"
                  @click="decide('approve', true)"
                >
                  {{ requests.busy ? 'Applying…' : 'Overwrite and apply' }}
                </button>
              </template>
            </div>
            <p v-if="canDecide" class="mt-2 text-[11px] text-slate-500">
              Approving runs the operations above against Kong, in dependency order, stopping at the first failure.
            </p>
          </footer>
        </div>
      </template>
    </section>
  </div>
</template>
