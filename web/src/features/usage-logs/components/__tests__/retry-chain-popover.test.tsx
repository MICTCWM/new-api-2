/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { fireEvent, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { beforeAll, describe, expect, test } from 'vitest'

import { RetryChainPopover } from '../retry-chain-popover'

describe('retry chain popover', () => {
  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      'Retry Chain': 'Retry Chain',
      'Failure Details': 'Failure Details',
      'Original Channel': 'Original Channel',
      'Destination Channel': 'Destination Channel',
      Reason: 'Reason',
      'No failure details available': 'No failure details available',
      'Status Code': 'Status Code',
      'Error Code': 'Error Code',
    })
  })

  test('shows the failed source and destination channel reason when opened', async () => {
    render(
      <RetryChainPopover
        channelIds={['12', '27']}
        failures={[
          {
            attempt_index: 0,
            channel_id: 12,
            channel_name: 'primary',
            status_code: 429,
            error_code: 'do_request_failed',
            reason: 'upstream quota exceeded',
          },
        ]}
      />
    )

    fireEvent.click(screen.getByRole('button', { name: 'Retry Chain' }))

    expect(await screen.findByText('Failure Details')).toBeInTheDocument()
    expect(screen.getByText('#12 (primary)')).toBeInTheDocument()
    expect(screen.getByText('#27')).toBeInTheDocument()
    expect(screen.getByText('upstream quota exceeded')).toBeInTheDocument()
  })
})
