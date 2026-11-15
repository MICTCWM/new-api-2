import { render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { beforeAll, describe, expect, test } from 'vitest'

import { ChannelRetryLabel } from '../channel-retry-label'

describe('channel retry label', () => {
  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      'No retry': 'No retry',
      'Retried {{count}} time(s)': 'Retried {{count}} time(s)',
    })
  })

  test('reads 否-equivalent when the request never retried', () => {
    render(<ChannelRetryLabel count={0} />)

    expect(screen.getByText('No retry')).toBeInTheDocument()
  })

  test('reads the retry count when the request retried', () => {
    render(<ChannelRetryLabel count={2} />)

    expect(screen.getByText('Retried 2 time(s)')).toBeInTheDocument()
  })
})
