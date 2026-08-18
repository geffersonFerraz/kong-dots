import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { reactive } from 'vue'
import SchemaForm from './SchemaForm.vue'

// Shaped like what Kong returns from /schemas/plugins/{name}.
const SCHEMA = {
  fields: [
    { protocols: { type: 'set', elements: { type: 'string' } } },
    {
      config: {
        type: 'record',
        fields: [
          { minute: { type: 'number', default: null } },
          { policy: { type: 'string', default: 'local', one_of: ['local', 'cluster', 'redis'] } },
          { fault_tolerant: { type: 'boolean', default: true } },
          { header_names: { type: 'array', elements: { type: 'string' } } },
          {
            redis: {
              type: 'record',
              fields: [{ host: { type: 'string' } }, { port: { type: 'integer', default: 6379 } }],
            },
          },
        ],
      },
    },
  ],
}

function fieldByLabel(wrapper, label) {
  const found = wrapper.findAll('label').find((l) => l.find('span').text() === label)
  if (!found) throw new Error(`no field labelled "${label}" (have: ${wrapper.findAll('label').map((l) => l.find('span').text())})`)
  return found
}

const lastUpdate = (wrapper) => wrapper.emitted('update').at(-1)[0]

describe('SchemaForm', () => {
  it('renders one input per config field, including nested records', () => {
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config: {} } })
    const labels = wrapper.findAll('label').map((l) => l.find('span').text())
    expect(labels).toContain('minute')
    expect(labels).toContain('policy')
    expect(labels).toContain('redis.host')
    // fields outside `config` are the entity's own, not the plugin config
    expect(labels).not.toContain('protocols')
  })

  // Regression: config arrives as a Pinia reactive Proxy. structuredClone throws
  // DataCloneError on a Proxy, which silently swallowed every config edit.
  it('emits the edit when config is a reactive proxy', async () => {
    const config = reactive({})
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config } })

    await fieldByLabel(wrapper, 'minute').find('input').setValue('17')

    expect(wrapper.emitted('update')).toBeTruthy()
    expect(lastUpdate(wrapper)).toEqual({ minute: 17 })
  })

  it('keeps the fields it is not editing', async () => {
    const config = reactive({ policy: 'local', limit_by: 'consumer', hour: null })
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config } })

    await fieldByLabel(wrapper, 'minute').find('input').setValue('5')

    expect(lastUpdate(wrapper)).toEqual({ policy: 'local', limit_by: 'consumer', hour: null, minute: 5 })
  })

  it('writes nested record fields into the right shape', async () => {
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config: reactive({ minute: 1 }) } })

    await fieldByLabel(wrapper, 'redis.host').find('input').setValue('redis.internal')

    expect(lastUpdate(wrapper)).toEqual({ minute: 1, redis: { host: 'redis.internal' } })
  })

  it('maps schema types onto the right controls', async () => {
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config: reactive({}) } })

    expect(fieldByLabel(wrapper, 'minute').find('input').attributes('type')).toBe('number')
    expect(fieldByLabel(wrapper, 'policy').find('select').exists()).toBe(true)
    expect(fieldByLabel(wrapper, 'fault_tolerant').find('button').exists()).toBe(true)
    expect(fieldByLabel(wrapper, 'header_names').find('textarea').exists()).toBe(true)
  })

  it('turns an emptied number field into null rather than a string', async () => {
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config: reactive({ minute: 10 }) } })
    await fieldByLabel(wrapper, 'minute').find('input').setValue('')
    expect(lastUpdate(wrapper)).toEqual({ minute: null })
  })

  it('splits a list field into an array', async () => {
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config: reactive({}) } })
    await fieldByLabel(wrapper, 'header_names').find('textarea').setValue('X-A\nX-B\n\n')
    expect(lastUpdate(wrapper)).toEqual({ header_names: ['X-A', 'X-B'] })
  })

  it('toggles a boolean field', async () => {
    const wrapper = mount(SchemaForm, { props: { schema: SCHEMA, config: reactive({ fault_tolerant: true }) } })
    await fieldByLabel(wrapper, 'fault_tolerant').find('button').trigger('click')
    expect(lastUpdate(wrapper)).toEqual({ fault_tolerant: false })
  })

  it('says so when the plugin has no config at all', () => {
    const wrapper = mount(SchemaForm, { props: { schema: { fields: [] }, config: {} } })
    expect(wrapper.text()).toMatch(/no configurable fields/i)
  })
})
