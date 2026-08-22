import { defineStore } from 'pinia'
import { api } from '../api/client'
import { useSessionStore } from './session'

// The queue of changes waiting for somebody allowed to push them to Kong.
// Nothing here has touched the gateway: a request is a canvas plus the state it
// was built on, kept until an approver runs it or turns it down.
export const useRequestsStore = defineStore('requests', {
  state: () => ({
    connectionId: null,
    list: [],
    // The request currently open for review, with a plan the backend rebuilt
    // against Kong just now — not the one recorded when it was filed.
    detail: null,
    loading: false,
    busy: false,
    error: null,
  }),

  getters: {
    pending: (s) => s.list.filter((r) => r.status === 'pending'),
    pendingCount() {
      return this.pending.length
    },
    decided: (s) => s.list.filter((r) => r.status !== 'pending'),

    // What the open request would do to Kong if approved right now.
    plan: (s) => s.detail?.plan ?? null,
    conflicts: (s) => s.detail?.plan?.conflicts ?? [],
  },

  actions: {
    async load(connectionId) {
      if (!connectionId) return
      this.connectionId = connectionId
      this.loading = true
      this.error = null
      try {
        const res = await api.listRequests(connectionId)
        this.list = res.requests ?? []
      } catch (e) {
        this.error = e.message
      } finally {
        this.loading = false
      }
    },

    async open(id) {
      this.detail = null
      this.error = null
      this.busy = true
      try {
        this.detail = await api.getRequest(this.connectionId, id)
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    close() {
      this.detail = null
      this.error = null
    },

    // decide runs one of the three verdicts and folds the result back into the
    // list, so the queue reflects it without a round trip.
    async decide(verdict, id, { note = '', force = false } = {}) {
      const session = useSessionStore()
      const body = { note, force, client_id: session.clientId }
      const call = {
        approve: () => api.approveRequest(this.connectionId, id, body),
        reject: () => api.rejectRequest(this.connectionId, id, body),
        withdraw: () => api.withdrawRequest(this.connectionId, id, body),
      }[verdict]

      this.busy = true
      this.error = null
      try {
        const res = await call()
        this.upsert(res.request)
        this.detail = null
        return res
      } catch (e) {
        this.error = e.message
        // A conflict comes back with the plan that was refused, so the reviewer
        // can see whose change is in the way before deciding to force it.
        if (e.status === 409 && e.body?.plan && this.detail) {
          this.detail = { ...this.detail, plan: e.body.plan }
        }
        return null
      } finally {
        this.busy = false
      }
    },

    approve(id, opts) {
      return this.decide('approve', id, opts)
    },
    reject(id, opts) {
      return this.decide('reject', id, opts)
    },
    withdraw(id, opts) {
      return this.decide('withdraw', id, opts)
    },

    // upsert folds in a request, whether it arrived over the socket because
    // somebody else filed it or came back from a call made here.
    upsert(request) {
      if (!request?.id) return
      const at = this.list.findIndex((r) => r.id === request.id)
      if (at === -1) this.list = [request, ...this.list]
      else this.list = this.list.map((r) => (r.id === request.id ? { ...r, ...request } : r))
    },

    reset() {
      this.$patch({ connectionId: null, list: [], detail: null, error: null })
    },
  },
})
