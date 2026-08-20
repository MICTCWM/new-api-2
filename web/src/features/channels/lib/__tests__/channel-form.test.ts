import { describe, it, expect } from 'vitest'
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
})