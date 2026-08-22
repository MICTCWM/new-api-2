import { GitBranch } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

import type { ChannelRetryFailure } from '../types'

interface RetryChainPopoverProps {
  channelIds: string[]
  failures?: ChannelRetryFailure[]
  compact?: boolean
}

function channelLabel(channelId: string, channelName?: string): string {
  return channelName ? `#${channelId} (${channelName})` : `#${channelId}`
}

export function RetryChainPopover(props: RetryChainPopoverProps) {
  const { t } = useTranslation()
  const failures = props.failures ?? []

  if (props.channelIds.length <= 1) return null

  return (
    <Popover>
      <PopoverTrigger
        render={
          <button
            type='button'
            className={cn(
              'text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex min-w-0 items-center gap-1 rounded-md transition-colors focus-visible:ring-2 focus-visible:outline-none',
              props.compact
                ? 'size-5 shrink-0 justify-center rounded-full'
                : 'max-w-full px-1.5 py-0.5 text-left text-xs'
            )}
            aria-label={t('Retry Chain')}
            onClick={(event) => event.stopPropagation()}
            title={t('Retry Chain')}
          />
        }
      >
        <GitBranch
          className={cn(
            'shrink-0 text-amber-500',
            props.compact ? 'size-3.5' : 'size-3'
          )}
          aria-hidden='true'
        />
        {!props.compact && (
          <span className='truncate font-mono'>
            {props.channelIds.join(' → ')}
          </span>
        )}
      </PopoverTrigger>
      <PopoverContent side='top' align='start' className='w-80 max-w-[90vw] text-xs'>
        <div className='flex min-w-0 flex-col gap-2'>
          <p className='font-medium'>{t('Retry Chain')}</p>
          <p className='text-muted-foreground font-mono break-all'>
            {props.channelIds.join(' → ')}
          </p>
          {failures.length > 0 ? (
            <div className='space-y-2 border-t pt-2'>
              <p className='font-medium'>{t('Failure Details')}</p>
              {failures.map((failure) => {
                const originalChannel = channelLabel(
                  String(failure.channel_id),
                  failure.channel_name
                )
                const nextChannel = props.channelIds[failure.attempt_index + 1]

                return (
                  <div
                    className='bg-muted/50 min-w-0 space-y-1 rounded-md p-2'
                    key={`${failure.attempt_index}-${failure.channel_id}`}
                  >
                    <p className='break-all'>
                      <span className='text-muted-foreground'>
                        {t('Original Channel')}:
                      </span>{' '}
                      <span className='font-mono'>{originalChannel}</span>
                    </p>
                    {nextChannel && (
                      <p className='break-all'>
                        <span className='text-muted-foreground'>
                          {t('Destination Channel')}:
                        </span>{' '}
                        <span className='font-mono'>#{nextChannel}</span>
                      </p>
                    )}
                    {failure.status_code != null && (
                      <p>
                        <span className='text-muted-foreground'>
                          {t('Status Code')}:
                        </span>{' '}
                        <span className='font-mono'>{failure.status_code}</span>
                      </p>
                    )}
                    {failure.error_code && (
                      <p className='break-all'>
                        <span className='text-muted-foreground'>
                          {t('Error Code')}:
                        </span>{' '}
                        <span className='font-mono'>{failure.error_code}</span>
                      </p>
                    )}
                    <p className='break-words whitespace-pre-wrap'>
                      <span className='text-muted-foreground'>
                        {t('Reason')}:
                      </span>{' '}
                      {failure.reason || t('No failure details available')}
                    </p>
                  </div>
                )
              })}
            </div>
          ) : (
            <p className='text-muted-foreground border-t pt-2'>
              {t('No failure details available')}
            </p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
