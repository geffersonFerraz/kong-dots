<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useGraphStore } from '../stores/graph'
import { KINDS, KIND_META } from '../api/entities'

const emit = defineEmits(['focus-node'])
const graph = useGraphStore()

const root = ref(null)
const input = ref(null)

// The dock stays a single search field until the user actually goes for it;
// only then does it grow the kind chips, the counter and the result list, so an
// idle canvas is not covered by controls nobody asked for.
const expanded = ref(false)

const results = computed(() => graph.filterResults)
const total = computed(() => graph.allNodes.length)

function open() {
  expanded.value = true
}

function collapse() {
  expanded.value = false
  input.value?.blur()
}

function clearAll() {
  graph.clearFilter()
  collapse()
}

function toggleKind(kind) {
  const kinds = graph.filter.kinds
  graph.setFilter({ kinds: kinds.includes(kind) ? kinds.filter((k) => k !== kind) : [...kinds, kind] })
  // Chips live inside the dock, so keep it open while they are being toggled.
  input.value?.focus()
}

function pick(result) {
  graph.selectedNodeId = result.nodeId
  emit('focus-node', result.nodeId)
  collapse()
}

// Anything outside the dock puts it away again; the query itself is kept, so an
// active filter survives while the user works on the canvas.
function onPointerDown(event) {
  if (expanded.value && root.value && !root.value.contains(event.target)) collapse()
}

// `/` opens the filter from anywhere on the canvas, like a search box should.
function onKeydown(event) {
  const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName)
  if (event.key === '/' && !typing) {
    event.preventDefault()
    open()
    input.value?.focus()
  }
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  window.addEventListener('mousedown', onPointerDown)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('mousedown', onPointerDown)
})
</script>

<template>
  <div ref="root" class="absolute left-3 top-3 z-20 text-sm" :class="expanded ? 'w-[300px]' : 'w-[210px]'" @click.stop>
    <div class="rounded-lg border border-[#2c3444] bg-[#171b24]/95 shadow-xl backdrop-blur">
      <div class="flex items-center gap-2 px-2.5 py-2">
        <svg
          viewBox="0 0 24 24"
          class="h-4 w-4 shrink-0 cursor-pointer text-slate-500 hover:text-slate-300"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          aria-label="Search the canvas"
          @click="open(); input?.focus()"
        >
          <circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" />
        </svg>
        <input
          ref="input"
          :value="graph.filter.query"
          class="w-full bg-transparent text-slate-100 placeholder:text-slate-500 focus:outline-none"
          :placeholder="expanded ? 'Filter by name, host, path or uuid…' : 'Filter…'"
          @focus="open"
          @input="graph.setFilter({ query: $event.target.value })"
          @keydown.esc="clearAll"
          @keydown.enter="results[0] && pick(results[0])"
        />
        <!-- Collapsed but filtering: say so, or the dimmed canvas looks broken. -->
        <span
          v-if="!expanded && graph.filterActive"
          class="shrink-0 rounded bg-sky-500/15 px-1.5 text-[10px] font-medium text-sky-300"
          :title="`${results.length} of ${total} nodes match`"
        >
          {{ results.length }}
        </span>
        <button
          v-if="graph.filterActive"
          class="shrink-0 text-slate-500 hover:text-slate-200"
          title="Clear filter"
          @click="clearAll"
        >
          ✕
        </button>
      </div>

      <template v-if="expanded">
        <div class="flex flex-wrap gap-1 border-t border-[#232a37] px-2.5 py-2">
          <button
            v-for="kind in KINDS"
            :key="kind"
            :aria-label="`Filter ${KIND_META[kind].singular}s`"
            :aria-pressed="graph.filter.kinds.includes(kind)"
            class="rounded border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide transition-colors"
            :style="
              graph.filter.kinds.includes(kind)
                ? { color: KIND_META[kind].accent, borderColor: KIND_META[kind].accent, backgroundColor: KIND_META[kind].accent + '22' }
                : {}
            "
            :class="graph.filter.kinds.includes(kind) ? '' : 'border-[#2c3444] text-slate-500 hover:text-slate-300'"
            @click="toggleKind(kind)"
          >
            {{ KIND_META[kind].singular }}
          </button>
        </div>

        <div v-if="graph.filterActive" class="flex items-center gap-2 border-t border-[#232a37] px-2.5 py-1.5 text-[11px]">
          <span :class="results.length ? 'text-slate-400' : 'text-amber-400'">
            {{ results.length }} of {{ total }} node{{ total === 1 ? '' : 's' }}
          </span>
          <label class="ml-auto flex cursor-pointer items-center gap-1.5 text-slate-400">
            <input
              type="checkbox"
              class="accent-sky-500"
              :checked="graph.filter.hideUnmatched"
              @change="graph.setFilter({ hideUnmatched: $event.target.checked })"
            />
            Hide the rest
          </label>
        </div>

        <ul v-if="graph.filterActive && results.length" class="max-h-64 overflow-y-auto border-t border-[#232a37] py-1 scroll-thin">
          <li v-for="r in results.slice(0, 50)" :key="r.nodeId">
            <button class="flex w-full items-center gap-2 px-2.5 py-1 text-left hover:bg-[#222835]" @click="pick(r)">
              <span class="h-1.5 w-1.5 shrink-0 rounded-full" :style="{ backgroundColor: KIND_META[r.kind].accent }" />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-slate-200">{{ r.label }}</span>
                <span v-if="r.detail" class="block truncate text-[10px] text-slate-500">{{ r.detail }}</span>
              </span>
              <span class="shrink-0 self-start font-mono text-[10px] text-slate-600">
                {{ r.draft ? 'new' : String(r.entityId).slice(0, 8) }}
              </span>
            </button>
          </li>
        </ul>
      </template>
    </div>
  </div>
</template>
