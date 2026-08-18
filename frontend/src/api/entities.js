// Descriptors for the Kong entity kinds the canvas knows about: how they look
// as nodes, which fields the property panel exposes and what a fresh one looks
// like. Plugin `config` is not described here — it is rendered from the schema
// Kong itself returns at /schemas/plugins/{name}.

export const DRAFT_PREFIX = 'draft:'

export const KINDS = ['services', 'routes', 'plugins', 'consumers', 'upstreams', 'targets']

export const KIND_META = {
  services: {
    singular: 'Service',
    accent: '#34d399',
    labelField: 'name',
    subtitle: (e) => `${e.protocol ?? 'http'}://${e.host ?? '?'}:${e.port ?? 80}${e.path ?? ''}`,
    defaults: () => ({ name: '', protocol: 'http', host: '', port: 80, path: '', retries: 5, connect_timeout: 60000, write_timeout: 60000, read_timeout: 60000, enabled: true, tags: [] }),
    fields: [
      { key: 'name', label: 'Name', type: 'text', required: true },
      { key: 'protocol', label: 'Protocol', type: 'select', options: ['http', 'https', 'grpc', 'grpcs', 'tcp', 'tls', 'udp', 'ws', 'wss'] },
      { key: 'host', label: 'Host', type: 'text', required: true, help: 'Hostname, IP or the name of an Upstream' },
      { key: 'port', label: 'Port', type: 'number' },
      { key: 'path', label: 'Path', type: 'text' },
      { key: 'retries', label: 'Retries', type: 'number' },
      { key: 'connect_timeout', label: 'Connect timeout (ms)', type: 'number' },
      { key: 'write_timeout', label: 'Write timeout (ms)', type: 'number' },
      { key: 'read_timeout', label: 'Read timeout (ms)', type: 'number' },
      { key: 'enabled', label: 'Enabled', type: 'boolean' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
  routes: {
    singular: 'Route',
    accent: '#38bdf8',
    labelField: 'name',
    subtitle: (e) => [(e.methods ?? []).join('|'), (e.paths ?? []).join(', ')].filter(Boolean).join(' ') || '(no matcher)',
    defaults: () => ({ name: '', protocols: ['http', 'https'], methods: [], hosts: [], paths: ['/'], strip_path: true, preserve_host: false, https_redirect_status_code: 426, path_handling: 'v0', request_buffering: true, response_buffering: true, tags: [] }),
    fields: [
      { key: 'name', label: 'Name', type: 'text' },
      { key: 'paths', label: 'Paths', type: 'string-list', help: 'Prefix or ~regex, one per line' },
      { key: 'methods', label: 'Methods', type: 'string-list' },
      { key: 'hosts', label: 'Hosts', type: 'string-list' },
      { key: 'protocols', label: 'Protocols', type: 'string-list' },
      { key: 'strip_path', label: 'Strip path', type: 'boolean' },
      { key: 'preserve_host', label: 'Preserve host', type: 'boolean' },
      { key: 'path_handling', label: 'Path handling', type: 'select', options: ['v0', 'v1'] },
      { key: 'regex_priority', label: 'Regex priority', type: 'number' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
  plugins: {
    singular: 'Plugin',
    accent: '#a78bfa',
    labelField: 'name',
    subtitle: (e) => (e.enabled === false ? 'disabled' : 'enabled'),
    defaults: () => ({ name: '', enabled: true, protocols: ['grpc', 'grpcs', 'http', 'https'], config: {}, tags: [] }),
    fields: [
      { key: 'name', label: 'Plugin', type: 'text', required: true, readonlyWhenSaved: true },
      { key: 'enabled', label: 'Enabled', type: 'boolean' },
      { key: 'protocols', label: 'Protocols', type: 'string-list' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
  consumers: {
    singular: 'Consumer',
    accent: '#fbbf24',
    labelField: 'username',
    subtitle: (e) => e.custom_id || e.username || '',
    defaults: () => ({ username: '', custom_id: '', tags: [] }),
    fields: [
      { key: 'username', label: 'Username', type: 'text' },
      { key: 'custom_id', label: 'Custom ID', type: 'text' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
  upstreams: {
    singular: 'Upstream',
    accent: '#fb7185',
    labelField: 'name',
    subtitle: (e) => e.algorithm ?? 'round-robin',
    defaults: () => ({ name: '', algorithm: 'round-robin', slots: 10000, tags: [] }),
    fields: [
      { key: 'name', label: 'Name', type: 'text', required: true, help: 'Services point at an Upstream by using this as their host' },
      { key: 'algorithm', label: 'Algorithm', type: 'select', options: ['round-robin', 'consistent-hashing', 'least-connections', 'latency'] },
      { key: 'slots', label: 'Slots', type: 'number' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
  targets: {
    singular: 'Target',
    accent: '#94a3b8',
    labelField: 'target',
    subtitle: (e) => `weight ${e.weight ?? 100}`,
    defaults: () => ({ target: '', weight: 100, tags: [] }),
    fields: [
      { key: 'target', label: 'Target', type: 'text', required: true, help: 'host:port' },
      { key: 'weight', label: 'Weight', type: 'number' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
}

// Which foreign key a plugin uses when attached to each kind of parent.
export const PLUGIN_PARENT_FIELD = {
  services: 'service',
  routes: 'route',
  consumers: 'consumer',
}

export const isDraftId = (id) => !id || String(id).startsWith(DRAFT_PREFIX)

export const nodeId = (kind, id) => `${kind}:${id}`

export const splitNodeId = (nid) => {
  const idx = nid.indexOf(':')
  return { kind: nid.slice(0, idx), id: nid.slice(idx + 1) }
}

export function entityLabel(kind, entity) {
  const meta = KIND_META[kind]
  return entity?.[meta?.labelField] || entity?.name || entity?.username || entity?.target || `(unnamed ${meta?.singular ?? kind})`
}

// Fields Kong accepts as a Route matcher; at least one must be set.
const ROUTE_MATCHERS = ['paths', 'hosts', 'methods', 'headers', 'snis']

const isBlank = (value) => {
  if (value === null || value === undefined) return true
  if (typeof value === 'string') return value.trim() === ''
  if (Array.isArray(value)) return value.length === 0
  if (typeof value === 'object') return Object.keys(value).length === 0
  return false
}

// validateEntity reports what Kong would reject, so the canvas can say it first
// instead of surfacing a schema violation halfway through an apply.
export function validateEntity(kind, entity) {
  const issues = []
  const add = (field, message) => issues.push({ field, message })

  for (const field of KIND_META[kind]?.fields ?? []) {
    if (field.required && isBlank(entity?.[field.key])) {
      add(field.key, `${field.label} is required`)
    }
  }

  switch (kind) {
    case 'routes':
      if (ROUTE_MATCHERS.every((m) => isBlank(entity?.[m]))) {
        add('paths', 'A Route needs at least one matcher: paths, hosts, methods, headers or snis')
      }
      break
    case 'consumers':
      if (isBlank(entity?.username) && isBlank(entity?.custom_id)) {
        add('username', 'A Consumer needs a username or a custom ID')
      }
      break
    case 'targets':
      if (!refId(entity, 'upstream')) {
        add('upstream', 'A Target must be attached to an Upstream')
      }
      break
    case 'services':
      if (!isBlank(entity?.port) && (Number(entity.port) < 1 || Number(entity.port) > 65535)) {
        add('port', 'Port must be between 1 and 65535')
      }
      break
  }
  return issues
}

export function refId(entity, field) {
  const v = entity?.[field]
  if (!v) return null
  return typeof v === 'string' ? v : (v.id ?? null)
}
