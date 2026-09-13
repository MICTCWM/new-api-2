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
import { RefreshCw, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { isHttpUrl } from '../lib'

type ProxySubscriptionCardProps = {
  configured: boolean
  subscriptionUrl: string
  fetchedAt: number
  draftUrl: string
  onDraftUrlChange: (value: string) => void
  savePending: boolean
  refreshPending: boolean
  clearPending: boolean
  onSave: () => void
  onRefresh: () => void
  onClear: () => void
}

/**
 * Card for managing the proxy subscription URL: prompts for a URL when none is
 * configured, otherwise shows the current URL + last fetch time with refresh
 * and clear actions.
 */
export function ProxySubscriptionCard(props: ProxySubscriptionCardProps) {
  const { t } = useTranslation()

  const lastFetched = props.fetchedAt
    ? new Date(props.fetchedAt * 1000).toLocaleString()
    : '—'

  if (!props.configured) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t('Proxy Subscription')}</CardTitle>
        </CardHeader>
        <CardContent className='grid gap-3'>
          <div className='grid gap-1.5'>
            <Label htmlFor='proxy-subscription-url'>
              {t('Subscription URL')}
            </Label>
            <Input
              id='proxy-subscription-url'
              type='url'
              placeholder='https://example.com/clash/sub'
              value={props.draftUrl}
              onChange={(e) => props.onDraftUrlChange(e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('Enter a subscription URL to pull proxy nodes from')}
            </p>
          </div>
          <div>
            <Button
              onClick={props.onSave}
              disabled={props.savePending || !isHttpUrl(props.draftUrl)}
            >
              {props.savePending
                ? t('Saving and pulling...')
                : t('Save and pull')}
            </Button>
          </div>
        </CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Proxy Subscription')}</CardTitle>
      </CardHeader>
      <CardContent className='grid gap-3'>
        <dl className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Subscription URL')}
            </dt>
            <dd className='min-w-0 break-all font-medium'>
              {props.subscriptionUrl}
            </dd>
          </div>
          <div className='grid gap-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Last pulled at')}
            </dt>
            <dd>{lastFetched}</dd>
          </div>
        </dl>
        <div className='flex flex-wrap gap-2'>
          <Button
            variant='outline'
            size='sm'
            onClick={props.onRefresh}
            disabled={props.refreshPending}
          >
            <RefreshCw
              className={`mr-1 h-3 w-3 ${
                props.refreshPending ? 'animate-spin' : ''
              }`}
            />
            {props.refreshPending ? t('Refreshing...') : t('Refresh subscription')}
          </Button>
          <Button
            variant='destructive'
            size='sm'
            onClick={props.onClear}
            disabled={props.clearPending}
          >
            <Trash2 className='mr-1 h-3 w-3' />
            {props.clearPending ? t('Clearing...') : t('Clear subscription')}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}