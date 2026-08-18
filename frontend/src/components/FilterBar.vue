<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useGraphStore } from '../stores/graph'
import { KINDS, KIND_META } from '../api/entities'

const emit = defineEmits(['focus-node'])
const graph = useGraphStore()

const input = ref(null)
const showResults = ref(false)

const results = computed(() => graph.filterResults)
const total = computed(() => graph.allNodes.length)

function toggleKind(kind) {
  const kinds = graph.filter.kinds
  graph.setFilter({ kinds: kinds.includes(kind) ? kinds.filter((k) => k !== kind) : [...kinds, kind] })
}

function pick(result) {
  graph.selectedNodeId = result.nodeId
  emit('focus-node', result.nodeId)
  showResults.value = false
}

// `/` focuses the filter from anywhere on the canvas, like a search box should.
function onKeydown(event) {
  const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName)
  if (event.key === '/' && !typing) {
    event.preventDefault()
    input.value?.focus()
  }
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="absolute left-3 top-3 z-20 w-[300px] text-sm" @click.stop>
    <div class="rounded-lg border border-[#2c3444] bg-[#171b24]/95 shadow-xl backdrop-blur">
      <div class="flex items-center gap-2 px-2.5 py-2">
        <svg viewBox="0 0 24 24" class="h-4 w-4 shrink-0 text-slate-500" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" />
        </svg>
        <input
          ref="input"
          :value="graph.filter.query"
          class="w-full bg-transparent text-slate-100 placeholder:text-slate-500 focus:outline-none"
          placeholder="Filter by name or uuid…"
          @input="graph.setFilter({ query: $event.target.value }); showResults = true"
          @focus="showResults = true"
          @keydown.esc="graph.clearFilter(); showResults = false"
          @keydown.enter="results[0] && pick(results[0])"
        />
        <button
          v-if="graph.filterActive"
          class="shrink-0 text-slate-500 hover:text-slate-200"
          title="Clear filter"
          @click="graph.clearFilter(); showResults = false"
        >
          ✕
        </button>
      </div>

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

      <ul
        v-if="showResults && graph.filterActive && results.length"
        class="max-h-64 overflow-y-auto border-t border-[#232a37] py-1 scroll-thin"
      >
        <li v-for="r in results.slice(0, 50)" :key="r.nodeId">
          <button class="flex w-full items-center gap-2 px-2.5 py-1 text-left hover:bg-[#222835]" @click="pick(r)">
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :style="{ backgroundColor: KIND_META[r.kind].accent }" />
            <span class="truncate text-slate-200">{{ r.label }}</span>
            <span class="ml-auto shrink-0 font-mono text-[10px] text-slate-600">
              {{ r.draft ? 'new' : String(r.entityId).slice(0, 8) }}
            </span>
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
