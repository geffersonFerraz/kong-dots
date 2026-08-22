import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../api/client', () => ({
  api: { me: vi.fn().mockResolvedValue({ actor: 'alice', approver: false, approval_required: true }) },
  setIdentity: vi.fn(),
}))

import { useSessionStore } from './session'

// A stand-in for the WebSocket, recording what the canvas tried to send.
function fakeSocket() {
  const sent = []
  return {
    sent,
    push: (type, payload) => sent.push({ type, payload }),
    announce: (payload) => sent.push({ type: 'presence', payload }),
    of: (type) => sent.filter((m) => m.type === type),
  }
}

function freshSession({ alone = false } = {}) {
  setActivePinia(createPinia())
  const session = useSessionStore()
  const socket = fakeSocket()
  session.attach(socket)
  // Pointers are only sent when there is somebody to see them, so a session
  // under test has company unless the test is about being alone.
  if (!alone) session.setPeers([{ id: session.clientId, name: 'me' }, { id: 'tab-b', name: 'bob' }])
  return { session, socket }
}

describe('pointers and drags shared over the socket', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    // The throttle lives in module scope, so each test starts by moving past
    // whatever the previous one left behind.
    vi.advanceTimersByTime(5000)
  })
  afterEach(() => vi.useRealTimers())

  it('sends the pointer in flow coordinates, at most 25 times a second', () => {
    const { session, socket } = freshSession()

    session.sendCursor(120, -40)
    session.sendCursor(121, -41) // same frame: not worth a message
    expect(socket.of('cursor')).toHaveLength(1)
    expect(socket.of('cursor')[0].payload).toEqual({ x: 120, y: -40 })

    vi.advanceTimersByTime(50)
    session.sendCursor(200, 10)
    expect(socket.of('cursor')).toHaveLength(2)
  })

  it('tells the others when the pointer leaves the canvas', () => {
    const { session, socket } = freshSession()

    session.sendCursor(10, 10)
    session.cursorLeft()

    const last = socket.of('cursor').at(-1)
    expect(last.payload.gone).toBe(true)
    // Leaving resets the throttle: coming back must show up immediately.
    session.sendCursor(11, 11)
    expect(socket.of('cursor')).toHaveLength(3)
  })

  it('never throttles away the frame that ends a drag', () => {
    const { session, socket } = freshSession()

    session.sendNodeMove('services:svc-1', 100, 100)
    session.sendNodeMove('services:svc-1', 104, 102) // mid-drag, same frame: dropped
    expect(socket.of('node_move')).toHaveLength(1)

    // The drop lands inside the same window and must still go out — it is the
    // position everyone else's canvas has to end up with.
    session.sendNodeMove('services:svc-1', 108, 105, true)
    const frames = socket.of('node_move')
    expect(frames).toHaveLength(2)
    expect(frames.at(-1).payload).toEqual({ node: 'services:svc-1', x: 108, y: 105, dropped: true })
  })

  it('sends nothing at all while nobody else is on this Kong', () => {
    const { session, socket } = freshSession({ alone: true })

    session.sendCursor(10, 10)
    session.sendNodeMove('services:svc-1', 1, 2, true)
    session.cursorLeft()

    expect(socket.sent).toEqual([])
  })

  it('does nothing at all before the socket is up', () => {
    setActivePinia(createPinia())
    const session = useSessionStore() // never attached
    expect(() => {
      session.sendCursor(1, 2)
      session.sendNodeMove('services:svc-1', 1, 2, true)
      session.announce('services:svc-1')
    }).not.toThrow()
  })
})

describe('other people on the canvas', () => {
  it('tracks each pointer by tab and ignores the echo of its own', () => {
    const { session } = freshSession()

    session.applyCursor({ id: 'tab-b', name: 'bob', x: 10, y: 20 })
    expect(session.cursors['tab-b']).toMatchObject({ name: 'bob', x: 10, y: 20 })

    session.applyCursor({ id: 'tab-b', name: 'bob', x: 30, y: 40 })
    expect(Object.keys(session.cursors)).toHaveLength(1)
    expect(session.cursors['tab-b']).toMatchObject({ x: 30, y: 40 })

    session.applyCursor({ id: session.clientId, name: 'me', x: 0, y: 0 })
    expect(session.cursors[session.clientId]).toBeUndefined()
  })

  it('drops a pointer that left the canvas', () => {
    const { session } = freshSession()
    session.applyCursor({ id: 'tab-b', name: 'bob', x: 10, y: 20 })
    session.applyCursor({ id: 'tab-b', name: 'bob', gone: true })
    expect(session.cursors['tab-b']).toBeUndefined()
  })

  it('forgets the pointer of somebody who disconnected', () => {
    const { session } = freshSession()
    session.applyCursor({ id: 'tab-b', name: 'bob', x: 10, y: 20 })
    session.applyCursor({ id: 'tab-c', name: 'carol', x: 1, y: 2 })

    // Bob's tab is gone from the roster: his pointer must not stay frozen.
    session.setPeers([
      { id: session.clientId, name: 'me' },
      { id: 'tab-c', name: 'carol' },
    ])

    expect(session.cursors['tab-b']).toBeUndefined()
    expect(session.cursors['tab-c']).toBeDefined()
  })

  it('groups peers by the node they have open, leaving out this browser', () => {
    const { session } = freshSession()
    session.setPeers([
      { id: session.clientId, name: 'me', node: 'services:svc-1' },
      { id: 'tab-b', name: 'bob', node: 'services:svc-1' },
      { id: 'tab-c', name: 'carol', node: 'routes:rt-1' },
      { id: 'tab-d', name: 'dave' },
    ])

    expect(session.others.map((p) => p.id)).toEqual(['tab-b', 'tab-c', 'tab-d'])
    expect(session.peersByNode['services:svc-1'].map((p) => p.name)).toEqual(['bob'])
    expect(session.peersByNode['routes:rt-1'].map((p) => p.name)).toEqual(['carol'])
    // Somebody with nothing open is on the canvas but marks no node.
    expect(Object.keys(session.peersByNode)).toHaveLength(2)
  })

  it('clears everyone when the socket goes away', () => {
    const { session } = freshSession()
    session.applyCursor({ id: 'tab-b', name: 'bob', x: 1, y: 1 })
    session.setPeers([{ id: 'tab-b', name: 'bob' }])

    session.detach()

    expect(session.cursors).toEqual({})
    expect(session.peers).toEqual([])
  })
})
