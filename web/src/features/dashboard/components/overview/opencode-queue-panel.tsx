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
import { useQuery } from '@tanstack/react-query'
import { Clock3, ListOrdered, RefreshCw, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { getOpencodeQueue } from '@/features/dashboard/api'
import { cn } from '@/lib/utils'

import { PanelWrapper } from '../ui/panel-wrapper'

function formatInteger(value: number) {
  return Number(value || 0).toLocaleString()
}

export function OpencodeQueuePanel() {
  const { t } = useTranslation()
  const queueQuery = useQuery({
    queryKey: ['dashboard', 'overview', 'opencode-queue'],
    queryFn: getOpencodeQueue,
    refetchInterval: 5000,
    staleTime: 3000,
    retry: false,
  })
  const snapshot = queueQuery.data?.data

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='info' size='sm'>
            <ListOrdered />
          </IconBadge>
          {t('Code provider request queue')}
        </span>
      }
      description={t('Live capacity and waiting requests for code providers')}
      loading={queueQuery.isLoading}
      height='h-auto'
      contentClassName='p-0'
      headerActions={
        <Button
          variant='ghost'
          size='sm'
          className='size-7 p-0'
          onClick={() => void queueQuery.refetch()}
          disabled={queueQuery.isFetching}
          aria-label={t('Refresh')}
          title={t('Refresh')}
        >
          <RefreshCw
            className={cn('size-3.5', queueQuery.isFetching && 'animate-spin')}
            aria-hidden='true'
          />
        </Button>
      }
    >
      {!snapshot ? (
        <div className='text-muted-foreground flex min-h-32 items-center justify-center px-4 text-sm'>
          {t('Queue data unavailable')}
        </div>
      ) : (
        <>
          <div className='bg-border grid grid-cols-2 gap-px border-b sm:grid-cols-4'>
            <QueueMetric
              label={t('Total capacity')}
              value={formatInteger(snapshot.total_capacity)}
            />
            <QueueMetric
              label={t('Available capacity')}
              value={formatInteger(snapshot.available_capacity)}
            />
            <QueueMetric
              label={t('Queued requests')}
              value={formatInteger(snapshot.queued_count)}
            />
            <QueueMetric
              label={t('Estimated wait')}
              value={t('{{minutes}} min', {
                minutes: snapshot.estimated_wait_minutes,
              })}
            />
          </div>
          {snapshot.items.length === 0 ? (
            <div className='text-muted-foreground flex min-h-24 items-center justify-center px-4 text-sm'>
              {t('No requests are waiting')}
            </div>
          ) : (
            <div className='divide-y'>
              {snapshot.items.map((item) => (
                <div
                  key={item.id}
                  className='grid gap-2 px-4 py-3 sm:grid-cols-[3rem_minmax(0,1fr)_10rem_7rem] sm:items-center sm:px-5'
                >
                  <div className='text-muted-foreground flex items-center gap-1.5 text-xs tabular-nums'>
                    <ListOrdered className='size-3.5' aria-hidden='true' />#
                    {item.position}
                  </div>
                  <div className='min-w-0'>
                    <div className='flex items-center gap-1.5 text-sm font-medium'>
                      <Users
                        className='text-muted-foreground size-3.5'
                        aria-hidden='true'
                      />
                      <span className='truncate'>
                        {item.username || `#${item.user_id}`}
                      </span>
                    </div>
                    <div className='text-muted-foreground truncate font-mono text-xs'>
                      {item.model}
                    </div>
                  </div>
                  <div className='text-muted-foreground text-xs sm:text-right'>
                    {t('{{tokens}} input tokens', {
                      tokens: formatInteger(item.input_tokens),
                    })}
                  </div>
                  <div className='text-muted-foreground flex items-center gap-1 text-xs sm:justify-end'>
                    <Clock3 className='size-3.5' aria-hidden='true' />
                    {new Date(item.queued_at * 1000).toLocaleTimeString()}
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </PanelWrapper>
  )
}

function QueueMetric(props: { label: string; value: string }) {
  return (
    <div className='bg-card min-w-0 px-3 py-2.5 sm:px-4'>
      <div className='text-muted-foreground truncate text-[11px] font-medium'>
        {props.label}
      </div>
      <div className='mt-1 font-mono text-sm font-semibold tabular-nums'>
        {props.value}
      </div>
    </div>
  )
}
