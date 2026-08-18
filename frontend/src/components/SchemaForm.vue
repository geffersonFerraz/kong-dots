<script setup>
import { computed } from 'vue'
import FieldInput from './FieldInput.vue'
import { deepClone } from '../api/clone'

// Renders a plugin's `config` from the JSON schema Kong returns at
// /schemas/plugins/{name}, so custom plugins and version differences are
// handled without the UI knowing anything about them.
const props = defineProps({
  schema: { type: Object, default: null },
  config: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['update'])

const fields = computed(() => (props.schema ? flatten(configFields(props.schema), '') : []))

function configFields(schema) {
  for (const entry of schema.fields ?? []) {
    const [name, def] = Object.entries(entry)[0] ?? []
    if (name === 'config') return def?.fields ?? []
  }
  return []
}

// flatten walks nested records into dotted paths (`limit.minute`), which keeps
// deeply nested plugin configs editable without a bespoke component per plugin.
function flatten(entries, prefix, depth = 0) {
  const out = []
  for (const entry of entries ?? []) {
    const pair = Object.entries(entry)[0]
    if (!pair) continue
    const [name, def] = pair
    const path = prefix ? `${prefix}.${name}` : name
    if (def?.type === 'record' && Array.isArray(def.fields) && depth < 2) {
      out.push({ group: true, key: path, label: path })
      out.push(...flatten(def.fields, path, depth + 1))
      continue
    }
    out.push({
      key: path,
      label: path,
      type: inputType(def),
      options: def?.one_of,
      required: !!def?.required,
      help: describe(def),
    })
  }
  return out
}

function inputType(def) {
  switch (def?.type) {
    case 'boolean':
      return 'boolean'
    case 'number':
    case 'integer':
      return 'number'
    case 'string':
      return Array.isArray(def.one_of) ? 'select' : 'text'
    case 'array':
    case 'set': {
      const el = def.elements?.type
      return el === 'string' || el === 'number' || el === 'integer' ? 'string-list' : 'json'
    }
    default:
      return 'json'
  }
}

function describe(def) {
  const bits = []
  if (def?.type) bits.push(def.type)
  if (def?.default !== undefined && def?.default !== null) bits.push(`default: ${JSON.stringify(def.default)}`)
  if (Array.isArray(def?.one_of) && def.one_of.length > 6) bits.push(`${def.one_of.length} options`)
  return bits.join(' · ')
}

function get(path) {
  return path.split('.').reduce((acc, key) => (acc == null ? undefined : acc[key]), props.config)
}

function set(path, value) {
  const next = deepClone(props.config) ?? {}
  const keys = path.split('.')
  let cur = next
  for (const key of keys.slice(0, -1)) {
    if (typeof cur[key] !== 'object' || cur[key] === null) cur[key] = {}
    cur = cur[key]
  }
  cur[keys.at(-1)] = value
  emit('update', next)
}
</script>

<template>
  <div v-if="!schema" class="text-xs text-slate-500">Loading plugin schema…</div>
  <div v-else-if="!fields.length" class="text-xs text-slate-500">This plugin has no configurable fields.</div>
  <div v-else class="space-y-3">
    <template v-for="field in fields" :key="field.key">
      <div v-if="field.group" class="pt-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
        {{ field.label }}
      </div>
      <FieldInput v-else :field="field" :model-value="get(field.key)" @update:model-value="set(field.key, $event)" />
    </template>
  </div>
</template>
