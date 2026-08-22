const BASE = import.meta.env.VITE_API_BASE ?? ''

// Who this browser says it is. There is no login yet: the name identifies the
// editor in the history and to the other people on the canvas, and the approval
// token — when the deployment uses one — is what actually grants the right to
// push a change to Kong.
let identity = { actor: '', token: '' }

export function setIdentity(next) {
  identity = { ...identity, ...next }
}

function identityHeaders() {
  const out = {}
  if (identity.actor) out['X-KongFlow-Actor'] = identity.actor
  if (identity.token) out['X-KongFlow-Approval-Token'] = identity.token
  return out
}

export class ApiError extends Error {
  constructor(message, status, body) {
    super(message)
    this.status = status
    this.body = body
  }
}

async function request(path, { method = 'GET', body, raw = false, headers = {} } = {}) {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: {
      ...identityHeaders(),
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...headers,
    },
    body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
  })
  if (raw) {
    if (!res.ok) throw new ApiError(await res.text(), res.status)
    return res.text()
  }
  if (res.status === 204) return null
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (!res.ok) {
    throw new ApiError(data?.error ?? res.statusText, res.status, data)
  }
  return data
}

export const api = {
  // Who the backend thinks this browser is, and whether it may apply directly.
  me: () => request('/api/me'),

  listConnections: () => request('/api/connections'),
  createConnection: (payload) => request('/api/connections', { method: 'POST', body: payload }),
  updateConnection: (id, payload) => request(`/api/connections/${id}`, { method: 'PUT', body: payload }),
  deleteConnection: (id) => request(`/api/connections/${id}`, { method: 'DELETE' }),
  testConnection: (payload) => request('/api/connections/test', { method: 'POST', body: payload }),
  status: (id) => request(`/api/connections/${id}/status`),

  getState: (id) => request(`/api/connections/${id}/state`),
  saveLayout: (id, positions) => request(`/api/connections/${id}/layout`, { method: 'PUT', body: { positions } }),
  pluginSchema: (id, name) => request(`/api/connections/${id}/schemas/plugins/${name}`),

  // Both carry the baseline the canvas was built on: without it the backend
  // cannot tell an entity the user deleted from one somebody else just added.
  plan: (id, body) => request(`/api/connections/${id}/plan`, { method: 'POST', body }),
  apply: (id, body) => request(`/api/connections/${id}/apply`, { method: 'POST', body }),

  // Change requests: what an editor's "Apply" becomes when they may not push to
  // Kong themselves.
  listRequests: (id, status) =>
    request(`/api/connections/${id}/requests${status ? `?status=${encodeURIComponent(status)}` : ''}`),
  getRequest: (id, reqId) => request(`/api/connections/${id}/requests/${reqId}`),
  submitRequest: (id, body) => request(`/api/connections/${id}/requests`, { method: 'POST', body }),
  approveRequest: (id, reqId, body) =>
    request(`/api/connections/${id}/requests/${reqId}/approve`, { method: 'POST', body }),
  rejectRequest: (id, reqId, body) =>
    request(`/api/connections/${id}/requests/${reqId}/reject`, { method: 'POST', body }),
  withdrawRequest: (id, reqId, body) =>
    request(`/api/connections/${id}/requests/${reqId}/withdraw`, { method: 'POST', body }),

  exportDeck: (id) => request(`/api/connections/${id}/export`, { raw: true }),
  importDeck: (id, yaml) =>
    request(`/api/connections/${id}/import`, {
      method: 'POST',
      body: yaml,
      headers: { 'Content-Type': 'application/yaml' },
    }),
  history: (id) => request(`/api/connections/${id}/history`),

  // Undoing a recorded run. The preview is rebuilt against Kong on every call,
  // so a run from last week is judged on today's gateway.
  rollbackPreview: (id, runId) => request(`/api/connections/${id}/history/${runId}/rollback`),
  rollback: (id, runId, body) =>
    request(`/api/connections/${id}/history/${runId}/rollback`, { method: 'POST', body }),
}

// openSocket subscribes to server events for a connection: apply progress, the
// roster of everyone else on the same Kong, and a nudge when somebody's change
// lands. `who` identifies this tab so the server can list it to the others.
export function openSocket(connectionId, who, onMessage) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const params = new URLSearchParams({
    connection_id: connectionId,
    client_id: who?.clientId ?? '',
    name: who?.name ?? '',
  })
  const ws = new WebSocket(`${proto}://${location.host}${BASE}/api/ws?${params}`)
  ws.onmessage = (ev) => {
    try {
      onMessage(JSON.parse(ev.data))
    } catch {
      /* ignore malformed frames */
    }
  }
  // Ephemeral collaboration frames — presence, pointers, drags. They only make
  // sense once the socket is up; before that they are dropped rather than
  // queued, because a pointer position from a second ago is worth nothing.
  ws.push = (type, payload) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type, payload }))
  }
  ws.announce = (payload) => ws.push('presence', payload)
  return ws
}
