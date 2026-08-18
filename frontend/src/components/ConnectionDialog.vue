<script setup>
import { computed, reactive, ref } from 'vue'
import { useConnectionsStore } from '../stores/connections'

const props = defineProps({ connection: { type: Object, default: null } })
const emit = defineEmits(['close', 'saved', 'deleted'])

const connections = useConnectionsStore()
const isEdit = computed(() => !!props.connection)

const form = reactive({
  name: props.connection?.name ?? '',
  admin_api_url: props.connection?.admin_api_url ?? 'http://localhost:8001',
  base_url: props.connection?.base_url ?? '',
  auth_type: props.connection?.auth_type ?? 'none',
  auth_secret: '',
  auth_header: props.connection?.auth_header ?? '',
  oauth_token_url: props.connection?.oauth_token_url ?? '',
  oauth_client_id: props.connection?.oauth_client_id ?? '',
  oauth_client_secret: '',
  workspace: props.connection?.workspace ?? '',
  environment: props.connection?.environment ?? 'dev',
  tags: props.connection?.tags ?? '',
  tls_skip_verify: props.connection?.tls_skip_verify ?? false,
})

const secretTouched = ref(false)
const oauthSecretTouched = ref(false)
const isOAuth = computed(() => form.auth_type === 'oauth2')
const testing = ref(false)
const saving = ref(false)
const testResult = ref(null)
const error = ref(null)
const confirmingDelete = ref(false)

const AUTH_TYPES = [
  { value: 'none', label: 'None' },
  { value: 'key', label: 'API key header' },
  { value: 'rbac', label: 'RBAC token (Enterprise)' },
  { value: 'bearer', label: 'Bearer token' },
  { value: 'basic', label: 'Basic (pre-encoded)' },
  { value: 'oauth2', label: 'OAuth2 — client credentials' },
]

// An untouched secret field on an existing connection means "keep the stored
// one", so the browser never has to hold a credential just to edit a name.
function payload() {
  const out = { ...form }
  if (isEdit.value && !secretTouched.value) delete out.auth_secret
  if (isEdit.value && !oauthSecretTouched.value) delete out.oauth_client_secret
  return out
}

const canSubmit = computed(() => {
  if (!form.name || !form.admin_api_url) return false
  if (isOAuth.value) return !!form.oauth_token_url && !!form.oauth_client_id
  return true
})

async function test() {
  testing.value = true
  testResult.value = null
  error.value = null
  try {
    testResult.value = await connections.test({ ...payload(), id: props.connection?.id })
  } catch (e) {
    error.value = e.message
  } finally {
    testing.value = false
  }
}

async function save() {
  saving.value = true
  error.value = null
  try {
    const saved = isEdit.value
      ? await connections.update(props.connection.id, payload())
      : await connections.create(payload())
    emit('saved', saved)
  } catch (e) {
    error.value = e.message
  } finally {
    saving.value = false
  }
}

async function remove() {
  saving.value = true
  try {
    await connections.remove(props.connection.id)
    emit('deleted', props.connection.id)
  } catch (e) {
    error.value = e.message
  } finally {
    saving.value = false
  }
}

const inputClass =
  'mt-1 w-full rounded-md border border-[#2a3140] bg-[#12161f] px-2.5 py-1.5 text-sm text-slate-100 ' +
  'placeholder:text-slate-500 focus:border-sky-500 focus:outline-none'
const labelClass = 'block text-[11px] font-medium uppercase tracking-wide text-slate-400'
</script>

<template>
  <div class="fixed inset-0 z-[70] grid place-items-center bg-black/60 p-4" @click.self="emit('close')">
    <div class="w-full max-w-xl rounded-xl border border-[#2c3444] bg-[#171b24] shadow-2xl">
      <header class="border-b border-[#2c3444] px-5 py-3">
        <h2 class="text-base font-semibold text-slate-100">
          {{ isEdit ? 'Edit Kong connection' : 'Register a Kong' }}
        </h2>
        <p class="text-xs text-slate-500">Credentials are encrypted at rest with AES-GCM before they touch the database.</p>
      </header>

      <div class="max-h-[65vh] space-y-3 overflow-y-auto px-5 py-4 scroll-thin">
        <div class="grid grid-cols-2 gap-3">
          <label :class="labelClass">
            Name
            <input v-model="form.name" :class="inputClass" placeholder="kong-prod-eu" />
          </label>
          <label :class="labelClass">
            Environment
            <select v-model="form.environment" :class="inputClass">
              <option value="dev">dev</option>
              <option value="staging">staging</option>
              <option value="prod">prod</option>
            </select>
          </label>
        </div>

        <label :class="labelClass">
          Admin API URL
          <input v-model="form.admin_api_url" :class="inputClass" placeholder="https://kong.example.com:8444" />
        </label>

        <label :class="labelClass">
          Proxy base URL
          <input v-model="form.base_url" :class="inputClass" placeholder="https://api.example.com (optional)" />
          <span class="mt-1 block text-[11px] text-slate-500">
            Where this Kong serves traffic. Used to build the URL a Route answers on, so it can be copied from the canvas.
          </span>
        </label>

        <div class="grid grid-cols-2 gap-3">
          <label :class="labelClass">
            Auth type
            <select v-model="form.auth_type" :class="inputClass">
              <option v-for="opt in AUTH_TYPES" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </label>
          <label v-if="form.auth_type === 'key'" :class="labelClass">
            Header name
            <input v-model="form.auth_header" :class="inputClass" placeholder="apikey" />
          </label>
        </div>

        <label v-if="form.auth_type !== 'none' && !isOAuth" :class="labelClass">
          Secret
          <input
            v-model="form.auth_secret"
            :class="inputClass"
            type="password"
            autocomplete="new-password"
            :placeholder="isEdit && connection.has_secret ? '•••••••• (stored — leave blank to keep)' : ''"
            @input="secretTouched = true"
          />
        </label>

        <div v-if="isOAuth" class="space-y-3 rounded-md border border-[#2a3140] bg-[#12161f] p-3">
          <p class="text-[11px] text-slate-500">
            A token is requested with <code class="text-slate-400">grant_type=client_credentials</code> and reused until
            it expires; Kong Dots renews it before every action once it runs out.
          </p>
          <label :class="labelClass">
            Token URL
            <input v-model="form.oauth_token_url" :class="inputClass" placeholder="https://idp.example.com/oauth2/token" />
          </label>
          <div class="grid grid-cols-2 gap-3">
            <label :class="labelClass">
              Client ID
              <input v-model="form.oauth_client_id" :class="inputClass" autocomplete="off" />
            </label>
            <label :class="labelClass">
              Client secret
              <input
                v-model="form.oauth_client_secret"
                :class="inputClass"
                type="password"
                autocomplete="new-password"
                :placeholder="isEdit && connection.has_oauth_secret ? '•••••••• (stored)' : ''"
                @input="oauthSecretTouched = true"
              />
            </label>
          </div>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <label :class="labelClass">
            Enterprise workspace
            <input v-model="form.workspace" :class="inputClass" placeholder="default (optional)" />
          </label>
          <label :class="labelClass">
            Tags
            <input v-model="form.tags" :class="inputClass" placeholder="edge, eu-west" />
          </label>
        </div>

        <label class="flex items-center gap-2 pt-1 text-sm text-slate-300">
          <input v-model="form.tls_skip_verify" type="checkbox" class="accent-sky-500" />
          Skip TLS verification (self-signed Admin API)
        </label>

        <div v-if="testResult" class="rounded-md border px-3 py-2 text-sm"
          :class="testResult.ok ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-200' : 'border-rose-500/40 bg-rose-500/10 text-rose-200'">
          <template v-if="testResult.ok">
            Connected — Kong {{ testResult.info.version }} ({{ testResult.info.edition }}),
            {{ testResult.info.plugins?.length ?? 0 }} plugins available.
            <div v-if="testResult.oauth" class="mt-1 text-xs text-emerald-300/80">
              Token obtained — {{ testResult.oauth.token_type }}, valid for {{ testResult.oauth.expires_in }}s{{
                testResult.oauth.scope ? `, scope: ${testResult.oauth.scope}` : ''
              }}.
            </div>
            <div v-if="testResult.workspaces?.length" class="mt-1 text-xs text-emerald-300/80">
              Workspaces: {{ testResult.workspaces.join(', ') }}
            </div>
          </template>
          <template v-else>
            <span v-if="testResult.stage === 'oauth'" class="font-medium">Authorization server rejected the request: </span>
            <span v-else-if="testResult.stage === 'admin_api'" class="font-medium">Token is fine, but the Admin API failed: </span>
            {{ testResult.error }}
          </template>
        </div>

        <p v-if="error" class="rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-200">
          {{ error }}
        </p>
      </div>

      <footer class="flex items-center gap-2 border-t border-[#2c3444] px-5 py-3">
        <button
          v-if="isEdit"
          class="rounded-md border border-rose-500/40 px-3 py-1.5 text-sm text-rose-300 hover:bg-rose-500/10"
          @click="confirmingDelete = true"
        >
          Delete
        </button>
        <span class="flex-1" />
        <button
          class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835] disabled:opacity-50"
          :disabled="testing || !form.admin_api_url"
          @click="test"
        >
          {{ testing ? 'Testing…' : 'Test connection' }}
        </button>
        <button class="rounded-md border border-[#2c3444] px-3 py-1.5 text-sm text-slate-300 hover:bg-[#222835]" @click="emit('close')">
          Cancel
        </button>
        <button
          class="rounded-md bg-sky-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-500 disabled:opacity-50"
          :disabled="saving || !canSubmit"
          @click="save"
        >
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
      </footer>

      <div v-if="confirmingDelete" class="border-t border-rose-500/30 bg-rose-500/5 px-5 py-3 text-sm text-rose-200">
        Remove “{{ connection.name }}” from Kong Dots? The Kong instance itself is untouched.
        <div class="mt-2 flex gap-2">
          <button class="rounded-md bg-rose-600 px-3 py-1 text-white hover:bg-rose-500" @click="remove">Yes, remove</button>
          <button class="rounded-md border border-[#2c3444] px-3 py-1 text-slate-300" @click="confirmingDelete = false">
            Cancel
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
