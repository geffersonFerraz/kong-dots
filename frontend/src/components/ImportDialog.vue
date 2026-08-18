<script setup>
import { ref } from 'vue'

const emit = defineEmits(['close', 'import'])
const yaml = ref('')
const busy = ref(false)

async function pickFile(event) {
  const file = event.target.files?.[0]
  if (file) yaml.value = await file.text()
}

async function submit() {
  busy.value = true
  try {
    await emit('import', yaml.value)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="fixed inset-0 z-[70] grid place-items-center bg-black/60 p-4" @click.self="emit('close')">
    <div class="flex w-full max-w-2xl flex-col rounded-xl border border-[#2c3444] bg-[#171b24] shadow-2xl">
      <header class="border-b border-[#2c3444] px-5 py-3">
        <h2 class="text-base font-semibold text-slate-100">Import a decK file</h2>
        <p class="text-xs text-slate-500">
          The document replaces the canvas contents. Nothing reaches Kong until you review and apply.
        </p>
      </header>
      <div class="space-y-3 px-5 py-4">
        <input type="file" accept=".yaml,.yml" class="text-xs text-slate-400" @change="pickFile" />
        <textarea
          v-model="yaml"
          rows="14"
          spellcheck="false"
          placeholder="_format_version: &quot;3.0&quot;&#10;services:&#10;  - name: api&#10;    host: api.internal"
          class="w-full rounded-md border border-[#2a3140] bg-[#12161f] px-3 py-2 font-mono text-xs text-slate-100 focus:border-sky-500 focus:outline-none"
        />
      </div>
      <footer class="flex justify-end gap-2 border-t border-[#2c3444] px-5 py-3">
        <button class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]" @click="emit('close')">
          Cancel
        </button>
        <button
          class="rounded-md bg-sky-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-500 disabled:opacity-50"
          :disabled="busy || !yaml.trim()"
          @click="submit"
        >
          Load onto canvas
        </button>
      </footer>
    </div>
  </div>
</template>
