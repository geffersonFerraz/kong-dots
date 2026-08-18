<script setup>
import { computed, ref, watch } from 'vue'
import ConfirmDialog from './ConfirmDialog.vue'
import FieldInput from './FieldInput.vue'
import SchemaForm from './SchemaForm.vue'
import { useGraphStore } from '../stores/graph'
import { KIND_META, entityLabel, isDraftId, refId } from '../api/entities'

const graph = useGraphStore()

const selection = computed(() => graph.selected)
const meta = computed(() => (selection.value ? KIND_META[selection.value.kind] : null))
const isPlugin = computed(() => selection.value?.kind === 'plugins')
const schema = computed(() => (isPlugin.value ? graph.pluginSchemas[selection.value.entity.name] : null))

// The plugin schema drives the config form, so fetch it whenever the selected
// plugin (or its name) changes.
watch(
  () => (isPlugin.value ? selection.value.entity.name : null),
  (name) => {
    if (name) graph.loadPluginSchema(name)
  },
  { immediate: true },
)

const attachedTo = computed(() => {
  if (!isPlugin.value) return null
  const e = selection.value.entity
  for (const [field, kind] of [
    ['service', 'services'],
    ['route', 'routes'],
    ['consumer', 'consumers'],
  ]) {
    const id = refId(e, field)
    if (!id) continue
    const parent = graph.entities[kind].find((p) => p.id === id)
    return { kind, label: parent ? entityLabel(kind, parent) : `${id} (missing)`, missing: !parent }
  }
  return { kind: null, label: 'Global (applies to all traffic)' }
})

const parentRef = computed(() => {
  const sel = selection.value
  if (!sel) return null
  if (sel.kind === 'routes') {
    const id = refId(sel.entity, 'service')
    const parent = graph.entities.services.find((s) => s.id === id)
    return { kind: 'services', label: parent ? entityLabel('services', parent) : 'Not attached to a Service' }
  }
  if (sel.kind === 'targets') {
    const id = refId(sel.entity, 'upstream')
    const parent = graph.entities.upstreams.find((u) => u.id === id)
    return { kind: 'upstreams', label: parent ? entityLabel('upstreams', parent) : 'Not attached to an Upstream' }
  }
  return null
})

// Validation problems for whatever is selected, keyed by the field they touch.
const issues = computed(() => graph.issuesByNode[graph.selectedNodeId] ?? [])
const errorFor = (key) => issues.value.find((i) => i.field === key)?.message ?? ''

function set(key, value) {
  graph.updateEntity(selection.value.kind, selection.value.id, { [key]: value })
}

function setConfig(config) {
  graph.updateEntity('plugins', selection.value.id, { config })
}

const confirming = ref(null)

function remove() {
  graph.deleteEntity(selection.value.kind, selection.value.id)
  confirming.value = null
}

function fieldDisabled(field) {
  return !!field.readonlyWhenSaved && !isDraftId(selection.value.entity.id)
}
</script>

<template>
  <aside class="flex h-full w-[340px] shrink-0 flex-col border-l border-[#232a37] bg-[#141821]">
    <div v-if="!selection" class="grid h-full place-items-center px-6 text-center text-sm text-slate-500">
      <div>
        <p>Select a node to edit its properties.</p>
        <p class="mt-2 text-xs">Right-click the canvas to create one.</p>
      </div>
    </div>

    <template v-else>
      <header class="border-b border-[#232a37] px-4 py-3">
        <div class="flex items-center gap-2">
          <span class="h-2 w-2 rounded-full" :style="{ backgroundColor: meta.accent }" />
          <span class="text-[10px] font-semibold uppercase tracking-wider" :style="{ color: meta.accent }">
            {{ meta.singular }}
          </span>
          <span
            v-if="isDraftId(selection.entity.id)"
            class="ml-auto rounded bg-sky-500/20 px-1.5 py-0.5 text-[9px] font-bold uppercase text-sky-300"
          >
            not applied
          </span>
        </div>
        <h2 class="mt-1 truncate text-base font-semibold text-slate-100">
          {{ entityLabel(selection.kind, selection.entity) }}
        </h2>
        <p v-if="!isDraftId(selection.entity.id)" class="truncate font-mono text-[10px] text-slate-500">
          {{ selection.entity.id }}
        </p>
      </header>

      <div class="flex-1 space-y-4 overflow-y-auto px-4 py-4 scroll-thin">
        <div v-if="attachedTo" class="rounded-md border border-[#2a3140] bg-[#12161f] px-3 py-2">
          <div class="text-[10px] uppercase tracking-wider text-slate-500">Attached to</div>
          <div class="text-sm" :class="attachedTo.missing ? 'text-rose-300' : 'text-slate-200'">
            {{ attachedTo.label }}
          </div>
          <button
            v-if="attachedTo.kind"
            class="mt-1 text-[11px] text-sky-400 hover:underline"
            @click="graph.updateEntity('plugins', selection.id, { service: null, route: null, consumer: null })"
          >
            Detach (make global)
          </button>
        </div>

        <div v-if="parentRef" class="rounded-md border border-[#2a3140] bg-[#12161f] px-3 py-2">
          <div class="text-[10px] uppercase tracking-wider text-slate-500">Parent</div>
          <div class="text-sm text-slate-200">{{ parentRef.label }}</div>
        </div>

        <div v-if="issues.length" class="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2">
          <div class="text-[10px] font-semibold uppercase tracking-wider text-amber-300">Kong would reject this</div>
          <ul class="mt-1 space-y-0.5 text-xs text-amber-200">
            <li v-for="issue in issues" :key="issue.field + issue.message">{{ issue.message }}</li>
          </ul>
        </div>

        <FieldInput
          v-for="field in meta.fields"
          :key="field.key"
          :field="field"
          :disabled="fieldDisabled(field)"
          :error="errorFor(field.key)"
          :model-value="selection.entity[field.key]"
          @update:model-value="set(field.key, $event)"
        />

        <template v-if="isPlugin">
          <div class="border-t border-[#232a37] pt-3">
            <div class="mb-2 flex items-center justify-between">
              <h3 class="text-[11px] font-semibold uppercase tracking-wider text-slate-400">Config</h3>
              <span class="font-mono text-[10px] text-slate-600">/schemas/plugins/{{ selection.entity.name }}</span>
            </div>
            <SchemaForm :schema="schema" :config="selection.entity.config ?? {}" @update="setConfig" />
          </div>
        </template>
      </div>

      <footer class="border-t border-[#232a37] px-4 py-3">
        <button
          class="w-full rounded-md border border-rose-500/40 px-3 py-1.5 text-sm text-rose-300 hover:bg-rose-500/10"
          @click="confirming = graph.cascade(selection.kind, selection.id)"
        >
          Delete {{ meta.singular }}
        </button>
      </footer>
    </template>

    <ConfirmDialog
      v-if="confirming"
      title="Delete from the canvas?"
      confirm-label="Remove"
      danger
      @cancel="confirming = null"
      @confirm="remove"
    >
      <p class="text-sm text-slate-300">
        These {{ confirming.length }} entit{{ confirming.length === 1 ? 'y' : 'ies' }} will be removed. Nothing is sent to
        Kong until you apply.
      </p>
      <ul class="mt-3 max-h-52 space-y-1 overflow-auto text-sm scroll-thin">
        <li v-for="v in confirming" :key="v.kind + v.id" class="flex items-center gap-2 text-slate-300">
          <span class="h-1.5 w-1.5 rounded-full" :style="{ backgroundColor: KIND_META[v.kind].accent }" />
          <span class="text-slate-500">{{ KIND_META[v.kind].singular }}</span>
          {{ v.label }}
        </li>
      </ul>
    </ConfirmDialog>
  </aside>
</template>
