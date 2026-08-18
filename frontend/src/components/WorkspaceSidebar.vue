<script setup>
import { computed } from 'vue'
import { useConnectionsStore } from '../stores/connections'

const emit = defineEmits(['select', 'create', 'edit'])
const connections = useConnectionsStore()

const ENV_STYLE = {
  prod: 'bg-rose-500/15 text-rose-300 border-rose-500/30',
  staging: 'bg-amber-500/15 text-amber-300 border-amber-500/30',
  dev: 'bg-sky-500/15 text-sky-300 border-sky-500/30',
}

const items = computed(() => connections.items)

function statusOf(id) {
  const s = connections.statuses[id]
  if (!s) return { color: 'bg-slate-500', title: 'Checking…' }
  return s.ok
    ? { color: 'bg-emerald-400', title: `Online — Kong ${s.info?.version ?? '?'} (${s.info?.edition ?? '?'})` }
    : { color: 'bg-rose-500', title: s.error ?? 'Unreachable' }
}
</script>

<template>
  <aside class="flex h-full w-64 shrink-0 flex-col border-r border-[#232a37] bg-[#141821]">
    <div class="flex items-center gap-2 border-b border-[#232a37] px-4 py-3">
      <div class="grid h-7 w-7 place-items-center rounded-md bg-emerald-500/15 text-emerald-400">
        <svg viewBox="0 0 24 24" class="h-4 w-4" fill="currentColor">
          <circle cx="6" cy="6" r="2.4" /><circle cx="18" cy="12" r="2.4" /><circle cx="6" cy="18" r="2.4" />
          <path d="M7.6 7.2l8.8 3.6M7.6 16.8l8.8-3.6" stroke="currentColor" stroke-width="1.4" fill="none" />
        </svg>
      </div>
      <div>
        <div class="text-sm font-semibold text-slate-100">Kong Dots</div>
        <div class="text-[10px] uppercase tracking-wider text-slate-500">Visual manager</div>
      </div>
    </div>

    <div class="flex items-center justify-between px-4 pb-1 pt-3">
      <span class="text-[10px] font-semibold uppercase tracking-wider text-slate-500">Workspaces</span>
      <button class="text-lg leading-none text-slate-400 hover:text-slate-100" title="Add a Kong" @click="emit('create')">
        +
      </button>
    </div>

    <nav class="flex-1 space-y-0.5 overflow-y-auto px-2 pb-3 scroll-thin">
      <p v-if="!items.length" class="px-2 py-6 text-center text-xs text-slate-500">
        No Kong registered yet.<br />
        <button class="mt-2 text-sky-400 hover:underline" @click="emit('create')">Register the first one</button>
      </p>

      <div
        v-for="conn in items"
        :key="conn.id"
        class="group cursor-pointer rounded-md px-2 py-2 transition-colors"
        :class="conn.id === connections.activeId ? 'bg-[#1e2430]' : 'hover:bg-[#1a1f2a]'"
        @click="emit('select', conn.id)"
      >
        <div class="flex items-center gap-2">
          <span class="h-2 w-2 shrink-0 rounded-full" :class="statusOf(conn.id).color" :title="statusOf(conn.id).title" />
          <span class="flex-1 truncate text-sm text-slate-200">{{ conn.name }}</span>
          <button
            class="hidden text-xs text-slate-500 hover:text-slate-200 group-hover:block"
            title="Edit connection"
            @click.stop="emit('edit', conn)"
          >
            edit
          </button>
        </div>
        <div class="mt-1 flex items-center gap-1.5 pl-4">
          <span class="rounded border px-1 text-[9px] font-semibold uppercase" :class="ENV_STYLE[conn.environment] ?? ENV_STYLE.dev">
            {{ conn.environment }}
          </span>
          <span v-if="conn.workspace" class="truncate text-[10px] text-slate-500">ws: {{ conn.workspace }}</span>
          <span v-else class="truncate text-[10px] text-slate-500">{{ conn.admin_api_url.replace(/^https?:\/\//, '') }}</span>
        </div>
      </div>
    </nav>
  </aside>
</template>
