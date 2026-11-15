import type { Table } from '@tanstack/react-table'
import type { RefObject } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePagination } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { formatNumber } from '@/lib/format'

import {
  formatConnectionDuration,
  getConnectionPresentation,
} from '../lib/connection-log-display'
import type { ConnectionLogItem } from '../types'
import {
  ConnectionEmptyState,
  ConnectionLiveIndicator,
  ConnectionRetryBadge,
  ConnectionTtftValue,
  ConnectionUserButton,
} from './connection-log-parts'
import { ConnectionLogsCompactRow } from './connection-logs-compact-row'
import { ConnectionOutputTokens } from './connection-output-tokens'
import { ModelBadge } from './model-badge'

/**
 * Mobile row: two compact lines. The first line identifies the request (user,
 * time, model, state), the second carries the live numbers so a single card
 * fits a few dozen pixels of vertical space.
 */
export function ConnectionLogsMobileCard(props: {
  item: ConnectionLogItem
  now: number
  /**
   * Drops the wording for open states: the beat/drop meter already carries
   * 正在连接 / 正在流式输出, and repeating it costs a badge on every row.
   * A finished request keeps 完成连接 so a settled row still reads as done.
   */
  hideActiveStatus?: boolean
}) {
  const { t } = useTranslation()
  const presentation = getConnectionPresentation(props.item, props.now, t)

  return (
    <div
      role='listitem'
      data-slot='connection-mobile-card'
      data-connection-request-id={props.item.request_id}
      className='animate-in fade-in-0 slide-in-from-right-4 border-border/40 overflow-hidden border-b px-2.5 py-1.5 duration-300 last:border-b-0 motion-reduce:animate-none'
    >
      <div className='flex min-w-0 items-center gap-2'>
        <ConnectionUserButton
          item={props.item}
          avatarOnly
          avatarClassName='size-5 **:data-[slot=avatar-fallback]:text-[10px]'
        />
        <ModelBadge
          compact
          modelName={props.item.model}
          className='max-w-[45%] min-w-0 shrink'
        />
        {/* The badge carries the full model name; the status cluster stays at
            the right edge and takes whatever width is left. */}
        <div
          data-slot='connection-mobile-status'
          className='ml-auto flex min-w-0 shrink items-center gap-1.5'
        >
          {props.hideActiveStatus && props.item.active ? (
            <span className='sr-only'>{t(presentation.meta.mainKey)}</span>
          ) : (
            <StatusBadge
              label={t(presentation.meta.mainKey)}
              variant={presentation.meta.variant}
              copyable={false}
            />
          )}
          <span className='flex shrink-0 items-center'>
            <ConnectionLiveIndicator
              item={props.item}
              tone={presentation.meta.variant}
            />
          </span>
        </div>
      </div>

      {/* Width budget on a 320px viewport: section `px-3` and card content
          `px-3` leave ~270px for the card, its own `px-2.5` leaves ~250px of
          row content, and `pl-7` leaves ~222px for the metrics. 10px type, 4px
          gaps and tight tracking keep the non-retry metrics on one line. */}
      <div
        data-slot='connection-mobile-metrics'
        className='text-muted-foreground mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1 gap-y-0.5 pl-7 font-mono text-[10px] leading-tight tracking-tighter tabular-nums sm:gap-x-2 sm:text-[11px] sm:tracking-normal'
      >
        <span className='whitespace-nowrap' title={t('Input')}>
          <span className='text-muted-foreground' aria-hidden='true'>
            ↑{' '}
          </span>
          <span className='sr-only'>{t('Input')} </span>
          {formatNumber(props.item.input_tokens)}
        </span>
        <span className='whitespace-nowrap' title={t('Output')}>
          <span className='text-muted-foreground' aria-hidden='true'>
            ↓{' '}
          </span>
          <span className='sr-only'>{t('Output')} </span>
          <ConnectionOutputTokens item={props.item} />
        </span>
        <span
          data-slot='connection-mobile-time-group'
          className='ml-auto flex shrink-0 items-center gap-1.5'
        >
          <span
            data-slot='connection-mobile-time-divider'
            className='bg-border h-3 w-px shrink-0'
            aria-hidden='true'
          />
          {props.item.stream ? (
            <span className='whitespace-nowrap' title={t('TTFT')}>
              <span className='sr-only'>{t('TTFT')} </span>
              <ConnectionTtftValue item={props.item} />
            </span>
          ) : null}
          <span className='whitespace-nowrap' title={t('Duration')}>
            {formatConnectionDuration(presentation.elapsed)}
          </span>
        </span>
        <ConnectionRetryBadge count={props.item.retry_count} />
        {presentation.retrying ? (
          <span className='text-warning whitespace-nowrap'>
            {t('Retrying in {{seconds}}s', {
              seconds: presentation.retrySeconds,
            })}
          </span>
        ) : null}
      </div>
    </div>
  )
}

/**
 * Mobile layout: the card list owns the scroll area, the empty state and the
 * pagination bar, so the table component only decides which layout to mount.
 */
export function ConnectionLogsMobileList(props: {
  /** Every row of the current page, used for the empty and pagination gates. */
  rows: ConnectionLogItem[]
  /** Rows on the current page, in display order. */
  visibleRows: ConnectionLogItem[]
  now: number
  scrollAreaRef: RefObject<HTMLDivElement | null>
  table: Table<ConnectionLogItem>
  /** Two-line cards without the open-state wording, or one-line rows. */
  layout?: 'list' | 'compact'
}) {
  const compact = props.layout === 'compact'

  return (
    <div className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border'>
      <div
        ref={props.scrollAreaRef}
        className='min-h-0 flex-1 overflow-y-auto overscroll-contain'
      >
        {props.rows.length === 0 ? (
          <div
            data-slot='connection-mobile-empty'
            className='flex h-full items-center justify-center'
          >
            <ConnectionEmptyState />
          </div>
        ) : (
          <div role='list'>
            {props.visibleRows.map((item) =>
              compact ? (
                <ConnectionLogsCompactRow
                  key={item.request_id}
                  item={item}
                  now={props.now}
                />
              ) : (
                <ConnectionLogsMobileCard
                  key={item.request_id}
                  item={item}
                  now={props.now}
                  hideActiveStatus
                />
              )
            )}
          </div>
        )}
      </div>
      {props.rows.length > 0 ? (
        <div className='shrink-0 border-t px-2 py-1.5 sm:px-3'>
          <DataTablePagination table={props.table} />
        </div>
      ) : null}
    </div>
  )
}
