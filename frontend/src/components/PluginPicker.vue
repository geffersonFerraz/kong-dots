<script setup>
import { computed, onMounted, ref } from 'vue'

const props = defineProps({ plugins: { type: Array, default: () => [] } })
const emit = defineEmits(['pick', 'close'])

const query = ref('')
const input = ref(null)

const results = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = q ? props.plugins.filter((p) => p.includes(q)) : props.plugins
  return list.slice(0, 200)
})

onMounted(() => input.value?.focus())
</script>

<template>
  <div class="fixed inset-0 z-[60] grid place-items-start justify-center bg-black/60 p-4 pt-24" @click.self="emit('close')">
    <div class="w-full max-w-md rounded-xl border border-[#2c3444] bg-[#171b24] shadow-2xl">
      <input
        ref="input"
        v-model="query"
        class="w-full rounded-t-xl border-b border-[#2c3444] bg-transparent px-4 py-3 text-sm text-slate-100 placeholder:text-slate-500 focus:outline-none"
        placeholder="Search plugins available on this Kong…"
        @keydown.esc="emit('close')"
        @keydown.enter="emit('pick', results[0] ?? query.trim())"
      />
      <div v-if="!plugins.length" class="px-4 py-6 text-sm text-slate-400">
        This Kong did not report its available plugins. Type a plugin name and press Enter.
      </div>
      <ul v-else class="max-h-80 overflow-auto py-1 scroll-thin">
        <li v-for="name in results" :key="name">
          <button class="w-full px-4 py-1.5 text-left text-sm text-slate-200 hover:bg-[#222835]" @click="emit('pick', name)">
            {{ name }}
          </button>
        </li>
        <li v-if="!results.length" class="px-4 py-3 text-sm text-slate-500">
          No match — press Enter to add “{{ query }}” anyway.
        </li>
      </ul>
    </div>
  </div>
</template>
