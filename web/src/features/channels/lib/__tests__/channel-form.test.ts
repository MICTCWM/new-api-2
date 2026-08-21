import { describe, it, expect } from 'vitest'

import { CHANNEL_TYPE_COMMAND, CHANNEL_TYPE_OPENCODE } from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToUpdatePayload,
} from '../channel-form'

describe('transformFormDataToUpdatePayload', () => {
  it('keeps group, priority and weight in the update payload', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      group: ['vip', 'cp'],
      priority: 5,
      weight: 2,
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.group).toBe('vip,cp')
    expect(payload.priority).toBe(5)
    expect(payload.weight).toBe(2)
  })

  it('keeps Command model mapping sources in the update payload', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_COMMAND,
      models: 'deepseek/deepseek-v4-flash,client-model',
      model_mapping: '{"client-model":"deepseek/deepseek-v4-flash"}',
      test_model: 'client-model',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek/deepseek-v4-flash,client-model')
    expect(payload.test_model).toBe('client-model')
  })

  it('keeps the Command fixed model when only a mapping source is selected', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_COMMAND,
      models: 'client-model',
      model_mapping: '{"client-model":"deepseek/deepseek-v4-flash"}',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek/deepseek-v4-flash,client-model')
  })

  it('keeps the optional Command Pro model when selected', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_COMMAND,
      models:
        'deepseek/deepseek-v4-flash,deepseek/deepseek-v4-pro,client-model',
      model_mapping: '{"client-model":"deepseek/deepseek-v4-pro"}',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe(
      'deepseek/deepseek-v4-flash,deepseek/deepseek-v4-pro,client-model'
    )
  })

  it('keeps OpenCode model mapping sources in the update payload', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_OPENCODE,
      models: 'deepseek-v4-flash,client-model',
      model_mapping: '{"client-model":"deepseek-v4-flash"}',
      test_model: 'client-model',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek-v4-flash,client-model')
    expect(payload.test_model).toBe('client-model')
  })

  it('keeps the OpenCode fixed model when only a mapping source is selected', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_OPENCODE,
      models: 'client-model',
      model_mapping: '{"client-model":"deepseek-v4-flash"}',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek-v4-flash,client-model')
  })
})
