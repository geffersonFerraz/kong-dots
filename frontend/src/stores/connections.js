import { defineStore } from 'pinia'
import { api } from '../api/client'

const ACTIVE_KEY = 'kong-flow:active-connection'

export const useConnectionsStore = defineStore('connections', {
  state: () => ({
    items: [],
    activeId: localStorage.getItem(ACTIVE_KEY) || null,
    statuses: {}, // id -> { ok, info, error, checkedAt }
    loading: false,
    error: null,
  }),

  getters: {
    active: (s) => s.items.find((c) => c.id === s.activeId) ?? null,
    activeStatus: (s) => (s.activeId ? s.statuses[s.activeId] : null),
  },

  actions: {
    async load() {
      this.loading = true
      this.error = null
      try {
        this.items = await api.listConnections()
        if (this.activeId && !this.items.some((c) => c.id === this.activeId)) {
          this.activeId = null
        }
        if (!this.activeId && this.items.length) this.select(this.items[0].id)
        this.items.forEach((c) => this.checkStatus(c.id))
      } catch (e) {
        this.error = e.message
      } finally {
        this.loading = false
      }
    },

    select(id) {
      this.activeId = id
      if (id) localStorage.setItem(ACTIVE_KEY, id)
      else localStorage.removeItem(ACTIVE_KEY)
    },

    async checkStatus(id) {
      try {
        const res = await api.status(id)
        this.statuses[id] = { ...res, checkedAt: new Date().toISOString() }
      } catch (e) {
        this.statuses[id] = { ok: false, error: e.message, checkedAt: new Date().toISOString() }
      }
    },

    async create(payload) {
      const created = await api.createConnection(payload)
      this.items = [...this.items, created].sort((a, b) => a.name.localeCompare(b.name))
      this.select(created.id)
      this.checkStatus(created.id)
      return created
    },

    async update(id, payload) {
      const updated = await api.updateConnection(id, payload)
      this.items = this.items.map((c) => (c.id === id ? updated : c))
      this.checkStatus(id)
      return updated
    },

    async remove(id) {
      await api.deleteConnection(id)
      this.items = this.items.filter((c) => c.id !== id)
      delete this.statuses[id]
      if (this.activeId === id) this.select(this.items[0]?.id ?? null)
    },

    test(payload) {
      return api.testConnection(payload)
    },
  },
})
