<script setup>
defineProps({
  title: { type: String, required: true },
  confirmLabel: { type: String, default: 'Confirm' },
  cancelLabel: { type: String, default: 'Cancel' },
  danger: { type: Boolean, default: false },
  busy: { type: Boolean, default: false },
})
defineEmits(['confirm', 'cancel'])
</script>

<template>
  <div class="fixed inset-0 z-[60] grid place-items-center bg-black/60 p-4" @click.self="$emit('cancel')">
    <div class="w-full max-w-lg rounded-xl border border-[#2c3444] bg-[#171b24] p-5 shadow-2xl">
      <h2 class="text-base font-semibold text-slate-100">{{ title }}</h2>
      <div class="mt-3"><slot /></div>
      <div class="mt-5 flex justify-end gap-2">
        <button
          class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]"
          @click="$emit('cancel')"
        >
          {{ cancelLabel }}
        </button>
        <button
          class="rounded-md px-3 py-1.5 text-sm font-medium text-white disabled:opacity-60"
          :class="danger ? 'bg-rose-600 hover:bg-rose-500' : 'bg-sky-600 hover:bg-sky-500'"
          :disabled="busy"
          @click="$emit('confirm')"
        >
          {{ busy ? 'Working…' : confirmLabel }}
        </button>
      </div>
    </div>
  </div>
</template>
