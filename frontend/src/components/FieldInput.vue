<script setup>
import { computed } from 'vue'

const props = defineProps({
  field: { type: Object, required: true },
  modelValue: { default: undefined },
  disabled: { type: Boolean, default: false },
  error: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue'])

const type = computed(() => props.field.type ?? 'text')

const listText = computed(() => (Array.isArray(props.modelValue) ? props.modelValue.join('\n') : ''))

const jsonText = computed(() => {
  if (props.modelValue === undefined || props.modelValue === null) return ''
  return typeof props.modelValue === 'string' ? props.modelValue : JSON.stringify(props.modelValue, null, 2)
})

function set(value) {
  emit('update:modelValue', value)
}

function setList(text) {
  const items = text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
  set(items)
}

function setNumber(raw) {
  if (raw === '') return set(null)
  const n = Number(raw)
  set(Number.isNaN(n) ? raw : n)
}

function setJson(text) {
  if (!text.trim()) return set(null)
  try {
    set(JSON.parse(text))
  } catch {
    // Keep the raw text so the user can finish typing; the panel flags it.
    set(text)
  }
}

const jsonInvalid = computed(() => type.value === 'json' && typeof props.modelValue === 'string' && props.modelValue.trim() !== '')

const inputClass = computed(
  () =>
    'w-full rounded-md bg-[#12161f] border px-2.5 py-1.5 text-sm text-slate-100 ' +
    'placeholder:text-slate-500 focus:outline-none disabled:opacity-50 ' +
    (props.error ? 'border-amber-500 focus:border-amber-400' : 'border-[#2a3140] focus:border-sky-500'),
)
</script>

<template>
  <label class="block">
    <span class="flex items-center gap-1 text-[11px] font-medium uppercase tracking-wide text-slate-400">
      {{ field.label ?? field.key }}
      <span v-if="field.required" class="text-rose-400">*</span>
    </span>

    <template v-if="type === 'boolean'">
      <button
        type="button"
        :disabled="disabled"
        class="mt-1 flex items-center gap-2 text-sm text-slate-200"
        @click="set(!modelValue)"
      >
        <span
          class="h-5 w-9 rounded-full transition-colors"
          :class="modelValue ? 'bg-emerald-500' : 'bg-slate-600'"
        >
          <span
            class="block h-4 w-4 translate-y-0.5 rounded-full bg-white transition-transform"
            :class="modelValue ? 'translate-x-4.5' : 'translate-x-0.5'"
          />
        </span>
        {{ modelValue ? 'true' : 'false' }}
      </button>
    </template>

    <select v-else-if="type === 'select'" :class="inputClass" class="mt-1" :value="modelValue ?? ''" :disabled="disabled" @change="set($event.target.value)">
      <option v-if="!field.required" value="">(unset)</option>
      <option v-for="opt in field.options ?? []" :key="opt" :value="opt">{{ opt }}</option>
    </select>

    <textarea
      v-else-if="type === 'string-list'"
      :class="inputClass"
      class="mt-1 font-mono text-xs"
      rows="3"
      :disabled="disabled"
      :value="listText"
      placeholder="one per line"
      @input="setList($event.target.value)"
    />

    <textarea
      v-else-if="type === 'json'"
      :class="[inputClass, jsonInvalid ? 'border-rose-500' : '']"
      class="mt-1 font-mono text-xs"
      rows="5"
      :disabled="disabled"
      :value="jsonText"
      @input="setJson($event.target.value)"
    />

    <input
      v-else-if="type === 'number'"
      :class="inputClass"
      class="mt-1"
      type="number"
      :disabled="disabled"
      :value="modelValue ?? ''"
      @input="setNumber($event.target.value)"
    />

    <input
      v-else
      :class="inputClass"
      class="mt-1"
      type="text"
      :disabled="disabled"
      :value="modelValue ?? ''"
      :placeholder="field.placeholder ?? ''"
      @input="set($event.target.value)"
    />

    <span v-if="jsonInvalid" class="mt-1 block text-[11px] text-rose-400">Not valid JSON yet</span>
    <span v-else-if="error" class="mt-1 block text-[11px] text-amber-400">{{ error }}</span>
    <span v-else-if="field.help" class="mt-1 block text-[11px] text-slate-500">{{ field.help }}</span>
  </label>
</template>
