<script setup>
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import { KIND_META, entityLabel } from '../api/entities'

const props = defineProps({
  data: { type: Object, required: true },
  selected: { type: Boolean, default: false },
})

const meta = computed(() => KIND_META[props.data.kind])
const label = computed(() => entityLabel(props.data.kind, props.data.entity))
const subtitle = computed(() => {
  try {
    return meta.value.subtitle(props.data.entity) ?? ''
  } catch {
    return ''
  }
})

// Which ports a node exposes mirrors the relations the Admin API allows.
const hasTarget = computed(() => ['services', 'routes', 'plugins', 'targets'].includes(props.data.kind))
const hasSource = computed(() => ['services', 'routes', 'consumers', 'upstreams'].includes(props.data.kind))

const issues = computed(() => props.data.issues ?? [])

// Other editors with this node open right now.
const peers = computed(() => props.data.peers ?? [])
const peersLabel = computed(() => peers.value.map((p) => p.name).join(', '))
const initials = (name) => (name ?? '?').trim().slice(0, 2).toUpperCase()

const attachment = computed(() => {
  const e = props.data.entity
  if (props.data.kind !== 'plugins') return null
  if (e.service) return 'on service'
  if (e.route) return 'on route'
  if (e.consumer) return 'on consumer'
  return 'global'
})
</script>

<template>
  <div
    class="kong-node w-[210px] overflow-hidden rounded-lg border bg-[#1b202b] shadow-lg shadow-black/40 transition-opacity"
    :class="[
      issues.length ? 'border-amber-400/80' : data.draft ? 'border-dashed border-sky-400/70' : 'border-[#2c3444]',
      data.dimmed ? 'opacity-25' : '',
      peers.length ? 'ring-1 ring-sky-400/50' : '',
    ]"
  >
    <Handle v-if="hasTarget" type="target" :position="Position.Left" />
    <div class="flex items-center gap-2 px-2 py-1.5" :style="{ backgroundColor: meta.accent + '22' }">
      <span class="h-2 w-2 shrink-0 rounded-full" :style="{ backgroundColor: meta.accent }" />
      <span class="text-[10px] font-semibold uppercase tracking-wider" :style="{ color: meta.accent }">
        {{ meta.singular }}
      </span>
      <span
        v-if="issues.length"
        class="ml-auto rounded bg-amber-500/20 px-1.5 text-[9px] font-bold uppercase text-amber-300"
        :title="issues.map((i) => i.message).join('\n')"
      >
        ⚠ incomplete
      </span>
      <span v-else-if="data.draft" class="ml-auto rounded bg-sky-500/20 px-1.5 text-[9px] font-bold uppercase text-sky-300">
        new
      </span>
      <span
        v-else-if="data.entity.enabled === false"
        class="ml-auto rounded bg-slate-600/40 px-1.5 text-[9px] font-bold uppercase text-slate-300"
      >
        off
      </span>
    </div>
    <div class="relative px-2.5 py-2">
      <div
        v-if="peers.length"
        class="absolute -top-2 right-2 flex gap-0.5"
        :title="`${peersLabel} ${peers.length === 1 ? 'has' : 'have'} this open`"
      >
        <span
          v-for="p in peers.slice(0, 3)"
          :key="p.id"
          class="grid h-4 w-4 place-items-center rounded-full bg-sky-500/80 text-[8px] font-bold text-white ring-1 ring-[#1b202b]"
        >
          {{ initials(p.name) }}
        </span>
      </div>
      <div class="truncate text-sm font-medium text-slate-100" :title="label">{{ label }}</div>
      <div class="truncate text-[11px] text-slate-400" :title="subtitle">{{ subtitle }}</div>
      <div v-if="attachment" class="mt-0.5 text-[10px] text-slate-500">{{ attachment }}</div>
    </div>
    <Handle v-if="hasSource" type="source" :position="Position.Right" />
  </div>
</template>
