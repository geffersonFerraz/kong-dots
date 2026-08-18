import { describe, expect, it } from 'vitest'
import { routeUrls, validateEntity } from './entities'

describe('routeUrls', () => {
  const base = 'https://api.example.com'

  it('joins the connection base URL with each path', () => {
    expect(routeUrls({ paths: ['/orders'] }, base)).toEqual(['https://api.example.com/orders'])
    expect(routeUrls({ paths: ['/orders', '/invoices'] }, base)).toEqual([
      'https://api.example.com/orders',
      'https://api.example.com/invoices',
    ])
  })

  it('does not produce a double slash, whatever the user typed', () => {
    expect(routeUrls({ paths: ['/orders'] }, 'https://api.example.com/')).toEqual(['https://api.example.com/orders'])
    expect(routeUrls({ paths: ['/orders'] }, 'https://api.example.com///')).toEqual(['https://api.example.com/orders'])
    expect(routeUrls({ paths: ['orders'] }, base)).toEqual(['https://api.example.com/orders'])
  })

  it('treats a Route with no path as the root', () => {
    expect(routeUrls({ paths: [] }, base)).toEqual(['https://api.example.com/'])
    expect(routeUrls({}, base)).toEqual(['https://api.example.com/'])
  })

  it('drops the regex marker so what is copied is an editable template', () => {
    expect(routeUrls({ paths: ['~/orders/\\d+'] }, base)).toEqual(['https://api.example.com/orders/\\d+'])
  })

  it('falls back to the Route hosts when the connection has no base URL', () => {
    expect(routeUrls({ paths: ['/orders'], hosts: ['api.internal'], protocols: ['http', 'https'] }, '')).toEqual([
      'https://api.internal/orders',
    ])
    expect(routeUrls({ paths: ['/orders'], hosts: ['api.internal'], protocols: ['http'] }, '')).toEqual([
      'http://api.internal/orders',
    ])
  })

  it('has nothing to offer when neither a base URL nor a host is known', () => {
    expect(routeUrls({ paths: ['/orders'] }, '')).toEqual([])
    expect(routeUrls({ paths: ['/orders'] }, undefined)).toEqual([])
  })

  it('prefers the base URL over the Route hosts', () => {
    expect(routeUrls({ paths: ['/orders'], hosts: ['internal.lan'] }, base)).toEqual([
      'https://api.example.com/orders',
    ])
  })

  it('de-duplicates and stays a menu-sized list', () => {
    expect(routeUrls({ paths: ['/a', '/a'] }, base)).toEqual(['https://api.example.com/a'])
    const many = routeUrls({ paths: Array.from({ length: 12 }, (_, i) => `/p${i}`) }, base)
    expect(many).toHaveLength(8)
  })
})

describe('validateEntity', () => {
  it('reports the field so the panel can mark it', () => {
    expect(validateEntity('services', { name: 'x' })).toEqual([
      { field: 'host', message: expect.stringMatching(/host is required/i) },
    ])
    expect(validateEntity('services', { name: 'x', host: 'h' })).toEqual([])
  })
})
