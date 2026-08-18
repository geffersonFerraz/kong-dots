import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('../api/client', () => ({
  api: {
    listConnections: vi.fn().mockResolvedValue([]),
    createConnection: vi.fn(),
    updateConnection: vi.fn(),
    deleteConnection: vi.fn(),
    testConnection: vi.fn(),
    status: vi.fn().mockResolvedValue({ ok: true }),
  },
  openSocket: vi.fn(),
}))

import { api } from '../api/client'
import ConnectionDialog from './ConnectionDialog.vue'

function mountDialog(connection = null) {
  setActivePinia(createPinia())
  return mount(ConnectionDialog, { props: { connection } })
}

function field(wrapper, label) {
  const found = wrapper.findAll('label').find((l) => l.text().trim().startsWith(label))
  if (!found) throw new Error(`no field "${label}" (have: ${wrapper.findAll('label').map((l) => l.text().split('\n')[0])})`)
  return found.find('input, select, textarea')
}

async function selectOAuth(wrapper) {
  await field(wrapper, 'Auth type').setValue('oauth2')
  return wrapper
}

const buttonNamed = (wrapper, text) => wrapper.findAll('button').find((b) => b.text() === text)

describe('ConnectionDialog — OAuth2 client credentials', () => {
  it('reveals the OAuth fields only for the oauth2 auth type', async () => {
    const wrapper = mountDialog()
    expect(wrapper.text()).not.toContain('Token URL')

    await selectOAuth(wrapper)
    expect(wrapper.text()).toContain('Token URL')
    expect(wrapper.text()).toContain('Client ID')
    expect(wrapper.text()).toContain('Client secret')
    expect(wrapper.text()).toContain('grant_type=client_credentials')
  })

  it('will not save until the token URL and client id are filled in', async () => {
    const wrapper = mountDialog()
    await field(wrapper, 'Name').setValue('oauth kong')
    await selectOAuth(wrapper)
    expect(buttonNamed(wrapper, 'Save').attributes('disabled')).toBeDefined()

    await field(wrapper, 'Token URL').setValue('https://idp.example.com/oauth2/token')
    expect(buttonNamed(wrapper, 'Save').attributes('disabled')).toBeDefined()

    await field(wrapper, 'Client ID').setValue('kong-dots')
    expect(buttonNamed(wrapper, 'Save').attributes('disabled')).toBeUndefined()
  })

  it('sends the OAuth settings when creating a connection', async () => {
    api.createConnection.mockResolvedValue({ id: 'c1', name: 'oauth kong' })
    const wrapper = mountDialog()

    await field(wrapper, 'Name').setValue('oauth kong')
    await field(wrapper, 'Admin API URL').setValue('https://kong.internal:8444')
    await selectOAuth(wrapper)
    await field(wrapper, 'Token URL').setValue('https://idp.example.com/oauth2/token')
    await field(wrapper, 'Client ID').setValue('kong-dots')
    await field(wrapper, 'Client secret').setValue('s3cr3t')
    await buttonNamed(wrapper, 'Save').trigger('click')

    expect(api.createConnection).toHaveBeenCalledWith(
      expect.objectContaining({
        auth_type: 'oauth2',
        oauth_token_url: 'https://idp.example.com/oauth2/token',
        oauth_client_id: 'kong-dots',
        oauth_client_secret: 's3cr3t',
      }),
    )
  })

  it('keeps the stored client secret when editing without retyping it', async () => {
    api.updateConnection.mockResolvedValue({ id: 'c1' })
    const wrapper = mountDialog({
      id: 'c1', name: 'oauth kong', admin_api_url: 'https://kong.internal:8444',
      auth_type: 'oauth2', oauth_token_url: 'https://idp/token', oauth_client_id: 'kong-dots',
      has_oauth_secret: true, environment: 'prod',
    })

    expect(field(wrapper, 'Client secret').attributes('placeholder')).toContain('stored')
    await field(wrapper, 'Name').setValue('renamed')
    await buttonNamed(wrapper, 'Save').trigger('click')

    const [, sent] = api.updateConnection.mock.calls.at(-1)
    expect(sent.name).toBe('renamed')
    expect(sent).not.toHaveProperty('oauth_client_secret')
    expect(sent.oauth_client_id).toBe('kong-dots')
  })

  it('sends a retyped client secret', async () => {
    api.updateConnection.mockResolvedValue({ id: 'c1' })
    const wrapper = mountDialog({
      id: 'c1', name: 'oauth kong', admin_api_url: 'https://kong.internal:8444',
      auth_type: 'oauth2', oauth_token_url: 'https://idp/token', oauth_client_id: 'kong-dots',
      has_oauth_secret: true,
    })

    await field(wrapper, 'Client secret').setValue('rotated')
    await buttonNamed(wrapper, 'Save').trigger('click')

    const [, sent] = api.updateConnection.mock.calls.at(-1)
    expect(sent.oauth_client_secret).toBe('rotated')
  })

  it('reports the token it obtained when the test succeeds', async () => {
    api.testConnection.mockResolvedValue({
      ok: true,
      info: { version: '3.9.1', edition: 'community', plugins: ['key-auth'] },
      oauth: { token_type: 'Bearer', expires_in: 3600, scope: 'kong:admin' },
    })
    const wrapper = mountDialog()
    await selectOAuth(wrapper)
    await field(wrapper, 'Token URL').setValue('https://idp/token')
    await buttonNamed(wrapper, 'Test connection').trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Token obtained')
    expect(wrapper.text()).toContain('Bearer')
    expect(wrapper.text()).toContain('3600')
    expect(wrapper.text()).toContain('kong:admin')
  })

  it('says whether it was the authorization server or the gateway that failed', async () => {
    api.testConnection.mockResolvedValue({ ok: false, stage: 'oauth', error: 'oauth: token endpoint returned 401: {"error":"invalid_client"}' })
    const wrapper = mountDialog()
    await selectOAuth(wrapper)
    await field(wrapper, 'Token URL').setValue('https://idp/token')
    await buttonNamed(wrapper, 'Test connection').trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Authorization server rejected the request')
    expect(wrapper.text()).toContain('invalid_client')
  })

  it('still supports the pre-existing auth types', async () => {
    api.createConnection.mockResolvedValue({ id: 'c1' })
    const wrapper = mountDialog()
    await field(wrapper, 'Name').setValue('rbac kong')
    await field(wrapper, 'Auth type').setValue('rbac')
    await field(wrapper, 'Secret').setValue('admin-token')
    await buttonNamed(wrapper, 'Save').trigger('click')

    expect(api.createConnection).toHaveBeenCalledWith(
      expect.objectContaining({ auth_type: 'rbac', auth_secret: 'admin-token' }),
    )
  })
})
