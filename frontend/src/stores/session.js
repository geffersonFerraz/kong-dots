import { defineStore } from 'pinia'
import { api, setIdentity } from '../api/client'

const KEY = 'kongflow.identity'

// Each browser tab is its own editor: two windows on the same Kong are two
// people as far as the canvas is concerned, and showing them separately is more
// honest than pretending one of them is not there.
const newClientId = () =>
  globalThis.crypto?.randomUUID?.() ?? `c-${Math.random().toString(36).slice(2, 10)}`

function readStored() {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '{}') ?? {}
  } catch {
    return {}
  }
}

// How often pointer and drag frames go out. 25 a second reads as continuous
// and costs one small frame per peer; sending on every mousemove event would be
// several times that for no visible gain.
const FRAME_MS = 40

let lastCursorAt = 0
let lastMoveAt = 0

function persist(state) {
  try {
    localStorage.setItem(KEY, JSON.stringify({ name: state.name, token: state.token }))
  } catch {
    /* private windows refuse; the name just does not survive a reload */
  }
}

export const useSessionStore = defineStore('session', {
  state: () => ({
    // The name shown to everyone else and recorded in the apply history. It is
    // self-declared: this tool has no login yet, so it identifies rather than
    // authenticates.
    name: '',
    // The shared secret that grants the right to push a change to Kong, when
    // the deployment is configured to require one.
    token: '',
    clientId: newClientId(),
    approver: true,
    approvalRequired: false,
    peers: [],
    // Other people's pointers, in flow coordinates, keyed by tab. Never
    // persisted and never sent to the backend's database — they die with the
    // socket.
    cursors: {},
    socket: null,
    ready: false,
  }),

  getters: {
    // What to show when somebody has not named themselves. An empty chip on
    // everyone else's screen helps nobody.
    displayName: (s) => s.name || `Guest ${s.clientId.slice(0, 4)}`,

    // Everyone else on this Kong right now.
    others: (s) => s.peers.filter((p) => p.id !== s.clientId),

    // Which nodes somebody else has open, so the canvas can mark them.
    peersByNode() {
      const out = {}
      for (const peer of this.others) {
        if (!peer.node) continue
        ;(out[peer.node] ??= []).push(peer)
      }
      return out
    },

    // True when this browser can only propose changes, not make them.
    proposesOnly: (s) => s.approvalRequired && !s.approver,

    // Whether this tab is the one that hands the canvas to a newcomer. The
    // roster is oldest first, so the longest-serving tab answers — otherwise a
    // room of five would send five copies of the same draft.
    answersCanvasRequests: (s) => s.peers.length > 0 && s.peers[0].id === s.clientId,
  },

  actions: {
    async init() {
      const stored = readStored()
      this.name = stored.name || ''
      this.token = stored.token || ''
      setIdentity({ actor: this.displayName, token: this.token })
      await this.refreshRole()
      this.ready = true
    },

    async refreshRole() {
      try {
        const me = await api.me()
        this.approver = !!me.approver
        this.approvalRequired = !!me.approval_required
        if (!this.name && me.actor && me.actor !== 'anonymous') this.name = me.actor
      } catch {
        // The role only decides which button is offered; the backend enforces
        // it regardless, so a failure here is not worth interrupting anyone.
      }
    },

    async setIdentity({ name, token }) {
      if (name !== undefined) this.name = name.trim()
      if (token !== undefined) this.token = token.trim()
      persist(this)
      setIdentity({ actor: this.displayName, token: this.token })
      await this.refreshRole()
      this.announce()
    },

    attach(socket) {
      this.socket = socket
      this.peers = []
      this.cursors = {}
      // A fresh socket starts with a clean throttle, so the first pointer frame
      // after a reconnect goes out immediately instead of being swallowed by a
      // window that belonged to the previous connection.
      lastCursorAt = 0
      lastMoveAt = 0
    },

    detach() {
      this.socket = null
      this.peers = []
      this.cursors = {}
    },

    // announce tells the other canvases who is here and what they have open.
    announce(node = null) {
      this.socket?.announce?.({ name: this.displayName, node: node ?? '' })
    },

    setPeers(peers) {
      this.peers = Array.isArray(peers) ? peers : []
      // Somebody who left must not leave their pointer frozen on the canvas.
      const live = new Set(this.peers.map((p) => p.id))
      const kept = {}
      for (const [id, cursor] of Object.entries(this.cursors)) {
        if (live.has(id)) kept[id] = cursor
      }
      this.cursors = kept
    },

    // ------------------------------------------------------ pointers, drags

    // sendCursor reports where this pointer is, in flow coordinates, so it
    // lands in the same place on a canvas panned and zoomed differently.
    sendCursor(x, y) {
      // Nobody to show it to: the common case is working alone, and a pointer
      // frame 25 times a second into an empty room is pure waste.
      if (!this.others.length) return
      const now = Date.now()
      if (now - lastCursorAt < FRAME_MS) return
      lastCursorAt = now
      this.socket?.push?.('cursor', { x, y })
    },

    // cursorLeft is sent when the pointer leaves the canvas, so the others drop
    // it instead of leaving it stuck at the edge.
    cursorLeft() {
      lastCursorAt = 0
      if (!this.others.length) return
      this.socket?.push?.('cursor', { x: 0, y: 0, gone: true })
    },

    // sendNodeMove streams a drag. The frame marked dropped is the one that
    // leaves the node where the user let go, so it always goes out — throttling
    // that one away would leave everyone else's canvas a few pixels off.
    sendNodeMove(node, x, y, dropped = false) {
      if (!this.others.length) return
      const now = Date.now()
      if (!dropped && now - lastMoveAt < FRAME_MS) return
      lastMoveAt = now
      this.socket?.push?.('node_move', { node, x, y, dropped })
    },

    // ------------------------------------------------- shared canvas draft

    // sendCanvasOp hands an edit to the other canvases. Unlike pointers, these
    // are never dropped: an edit that does not arrive leaves the two canvases
    // disagreeing about what is on them.
    sendCanvasOp(values, positions) {
      if (!this.others.length) return
      if (!values?.length && !positions?.length) return
      this.socket?.push?.('canvas_op', { changes: values, positions })
    },

    // requestCanvas asks whoever is already here for the shared draft, so a tab
    // that has just opened does not start from Kong's state alone and wipe out
    // work in progress the moment it edits anything.
    requestCanvas() {
      if (!this.others.length) return false
      this.socket?.push?.('state_request', {})
      return true
    },

    sendCanvas(snapshot) {
      this.socket?.push?.('state_sync', snapshot)
    },

    applyCursor(payload) {
      if (!payload?.id || payload.id === this.clientId) return
      if (payload.gone) {
        const { [payload.id]: _gone, ...rest } = this.cursors
        this.cursors = rest
        return
      }
      this.cursors = {
        ...this.cursors,
        [payload.id]: { id: payload.id, name: payload.name, x: payload.x, y: payload.y },
      }
    },
  },
})
