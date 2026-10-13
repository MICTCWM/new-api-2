import { describe, it, expect } from 'vitest'

import { CHANNEL_TYPE_COMMAND, CHANNEL_TYPE_OPENCODE } from '../../constants'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
} from '../channel-form'
import type { Channel } from '../../types'

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

  it('allows OpenCode glm-5.3-flash without forcing deepseek-v4-flash', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_OPENCODE,
      models: 'glm-5.3-flash',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('glm-5.3-flash')
  })

  it('allows both OpenCode native models', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_OPENCODE,
      models: 'deepseek-v4-flash,glm-5.3-flash',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek-v4-flash,glm-5.3-flash')
  })

  it('allows OpenCode deepseek-v4.1-flash without forcing deepseek-v4-flash', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_OPENCODE,
      models: 'deepseek-v4.1-flash',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek-v4.1-flash')
  })

  it('allows OpenCode deepseek-v4-pro without forcing deepseek-v4-flash', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_OPENCODE,
      models: 'deepseek-v4-pro',
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe('deepseek-v4-pro')
  })

  it('serializes a custom concurrency limit for ordinary channels', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      channel_concurrency_limit: 120,
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)
    const settings = JSON.parse(payload.settings as string) as Record<
      string,
      unknown
    >

    expect(settings.channel_concurrency_limit).toBe(120)
  })

  it('omits the ordinary channel concurrency setting for fixed code channels', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_COMMAND,
      channel_concurrency_limit: 120,
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)
    const settings = JSON.parse(payload.settings as string) as Record<
      string,
      unknown
    >

    expect(settings.channel_concurrency_limit).toBeUndefined()
  })

  it('persists the station cache toggle into the settings JSON for any channel', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      station_cache_enabled: true,
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)
    const settings = JSON.parse(payload.settings as string) as Record<
      string,
      unknown
    >

    expect(settings.station_cache_enabled).toBe(true)
  })

  it('persists the video frame extraction toggle into the settings JSON', () => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      video_frame_extraction_enabled: true,
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)
    const settings = JSON.parse(payload.settings as string) as Record<
      string,
      unknown
    >

    expect(settings.video_frame_extraction_enabled).toBe(true)
  })

  it('defaults the station cache toggle to off when the setting is absent', () => {
    const channel = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      id: 1,
      type: 1,
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      settings: '{}',
      group: 'default',
      channel_info: {} as Channel['channel_info'],
    } as unknown as Channel

    const formData = transformChannelToFormDefaults(channel)

    expect(formData.station_cache_enabled).toBe(false)
  })

  it('round-trips an enabled station cache toggle from the settings JSON', () => {
    const channel = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      id: 1,
      type: 1,
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      settings: JSON.stringify({ station_cache_enabled: true }),
      group: 'default',
      channel_info: {} as Channel['channel_info'],
    } as unknown as Channel

    const formData = transformChannelToFormDefaults(channel)

    expect(formData.station_cache_enabled).toBe(true)
  })

  it('round-trips an enabled video frame extraction toggle from the settings JSON', () => {
    const channel = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      id: 1,
      type: 1,
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      settings: JSON.stringify({ video_frame_extraction_enabled: true }),
      group: 'default',
      channel_info: {} as Channel['channel_info'],
    } as unknown as Channel

    const formData = transformChannelToFormDefaults(channel)

    expect(formData.video_frame_extraction_enabled).toBe(true)
  })
})

describe('Command model combinations', () => {
  const commandModels = {
    v4Flash: 'deepseek/deepseek-v4-flash',
    v4Pro: 'deepseek/deepseek-v4-pro',
    v41Flash: 'deepseek/deepseek-v4.1-flash',
  }

  it.each([
    {
      name: 'v4.1 flash with v4 flash',
      models: `${commandModels.v4Flash},${commandModels.v41Flash}`,
    },
    {
      name: 'v4 flash with pro',
      models: `${commandModels.v4Flash},${commandModels.v4Pro}`,
    },
    { name: 'v4 flash only', models: commandModels.v4Flash },
    { name: 'v4.1 flash only', models: commandModels.v41Flash },
    {
      name: 'v4.1 flash with pro',
      models: `${commandModels.v41Flash},${commandModels.v4Pro}`,
    },
  ])('accepts $name and keeps it unchanged', ({ models }) => {
    const formData = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: CHANNEL_TYPE_COMMAND,
      models,
    }

    const payload = transformFormDataToUpdatePayload(formData, 1)

    expect(payload.models).toBe(models)
  })

  it('rejects a pro-only Command channel', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'command',
      type: CHANNEL_TYPE_COMMAND,
      models: commandModels.v4Pro,
    })

    expect(result.success).toBe(false)
    const modelsIssue = result.success
      ? undefined
      : result.error.issues.find((issue) => issue.path[0] === 'models')
    expect(modelsIssue?.message).toBe(
      'Command must include deepseek/deepseek-v4-flash or deepseek/deepseek-v4.1-flash'
    )
  })

  it('accepts v4.1 flash on its own without forcing pro', () => {
    const result = channelFormSchema.safeParse({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'command',
      type: CHANNEL_TYPE_COMMAND,
      models: commandModels.v41Flash,
    })

    expect(result.success).toBe(true)
  })
})
