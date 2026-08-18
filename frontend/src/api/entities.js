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
    renameOnDuplicate: true,
    searchFields: ['name', 'host'],
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
    renameOnDuplicate: true,
    searchFields: ['name', 'paths', 'hosts', 'methods'],
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
    searchFields: ['name'],
    subtitle: (e) => (e.enabled === false ? 'disabled' : 'enabled'),
    defaults: () => ({ name: '', enabled: true, protocols: ['grpc', 'grpcs', 'http', 'https'], config: {}, tags: [] }),
    fields: [
      { key: 'name', label: 'Plugin', type: 'select', required: true, readonlyWhenSaved: true, optionsFrom: 'plugins' },
      { key: 'enabled', label: 'Enabled', type: 'boolean' },
      { key: 'protocols', label: 'Protocols', type: 'string-list' },
      { key: 'tags', label: 'Tags', type: 'string-list' },
    ],
  },
  consumers: {
    singular: 'Consumer',
    accent: '#fbbf24',
    labelField: 'username',
    renameOnDuplicate: true,
    searchFields: ['username', 'custom_id'],
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
    renameOnDuplicate: true,
    searchFields: ['name'],
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
    searchFields: ['target'],
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

// Kong's own uniqueness constraints, expressed as the key it enforces. Two
// entities sharing a key are exactly the 409 the Admin API would answer with.
export function uniqueKeys(kind, entity) {
  switch (kind) {
    case 'services':
    case 'routes':
    case 'upstreams':
      return entity?.name ? [{ key: `${kind}:name:${entity.name}`, field: 'name', what: 'name' }] : []
    case 'consumers': {
      const keys = []
      if (entity?.username) keys.push({ key: `consumers:username:${entity.username}`, field: 'username', what: 'username' })
      if (entity?.custom_id) keys.push({ key: `consumers:custom_id:${entity.custom_id}`, field: 'custom_id', what: 'custom ID' })
      return keys
    }
    case 'plugins': {
      if (!entity?.name) return []
      // One plugin of each type per scope, the scope being a Service, a Route,
      // a Consumer — or nothing at all, which is global.
      const scope = ['service', 'route', 'consumer'].map((f) => `${f}=${refId(entity, f) ?? ''}`).join('|')
      return [{ key: `plugins:${entity.name}:${scope}`, field: 'name', what: 'plugin' }]
    }
    default:
      return []
  }
}

// Kong 3.x marks a regex path with a leading "~". It cannot yield one concrete
// URL, so the marker is dropped and what is left is copied as a template.
const cleanPath = (path) => {
  const p = String(path ?? '').replace(/^~/, '')
  return p.startsWith('/') ? p : `/${p}`
}

// routeUrls builds the public URLs a Route answers on. The connection's base URL
// wins when set; otherwise the Route's own hosts are used, which is all Kong
// knows about where it is reachable.
export function routeUrls(route, baseUrl) {
  const paths = route?.paths?.length ? route.paths : ['/']
  const trimmed = String(baseUrl ?? '').trim().replace(/\/+$/, '')

  let origins = []
  if (trimmed) {
    origins = [trimmed]
  } else if (route?.hosts?.length) {
    const protocols = route.protocols ?? []
    const scheme = protocols.includes('https') || !protocols.includes('http') ? 'https' : 'http'
    origins = route.hosts.map((host) => `${scheme}://${host}`)
  }
  if (!origins.length) return []

  const urls = []
  for (const origin of origins) {
    for (const path of paths) {
      const url = origin + cleanPath(path)
      if (!urls.includes(url)) urls.push(url)
    }
  }
  return urls.slice(0, 8)
}

export function refId(entity, field) {
  const v = entity?.[field]
  if (!v) return null
  return typeof v === 'string' ? v : (v.id ?? null)
}
