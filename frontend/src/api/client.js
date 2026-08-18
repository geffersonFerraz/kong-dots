const BASE = import.meta.env.VITE_API_BASE ?? ''

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
    headers: body !== undefined ? { 'Content-Type': 'application/json', ...headers } : headers,
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
  listConnections: () => request('/api/connections'),
  createConnection: (payload) => request('/api/connections', { method: 'POST', body: payload }),
  updateConnection: (id, payload) => request(`/api/connections/${id}`, { method: 'PUT', body: payload }),
  deleteConnection: (id) => request(`/api/connections/${id}`, { method: 'DELETE' }),
  testConnection: (payload) => request('/api/connections/test', { method: 'POST', body: payload }),
  status: (id) => request(`/api/connections/${id}/status`),

  getState: (id) => request(`/api/connections/${id}/state`),
  saveLayout: (id, positions) => request(`/api/connections/${id}/layout`, { method: 'PUT', body: { positions } }),
  pluginSchema: (id, name) => request(`/api/connections/${id}/schemas/plugins/${name}`),

  plan: (id, desired) => request(`/api/connections/${id}/plan`, { method: 'POST', body: { desired } }),
  apply: (id, desired) => request(`/api/connections/${id}/apply`, { method: 'POST', body: { desired } }),

  exportDeck: (id) => request(`/api/connections/${id}/export`, { raw: true }),
  importDeck: (id, yaml) =>
    request(`/api/connections/${id}/import`, {
      method: 'POST',
      body: yaml,
      headers: { 'Content-Type': 'application/yaml' },
    }),
  history: (id) => request(`/api/connections/${id}/history`),
}

// openSocket subscribes to server events for a connection (apply progress).
export function openSocket(connectionId, onMessage) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const url = `${proto}://${location.host}${BASE}/api/ws?connection_id=${encodeURIComponent(connectionId)}`
  const ws = new WebSocket(url)
  ws.onmessage = (ev) => {
    try {
      onMessage(JSON.parse(ev.data))
    } catch {
      /* ignore malformed frames */
    }
  }
  return ws
}
