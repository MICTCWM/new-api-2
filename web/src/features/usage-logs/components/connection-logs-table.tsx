import {
  getCoreRowModel,
  getPaginationRowModel,
  useReactTable,
  type PaginationState,
} from '@tanstack/react-table'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePagination } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useMediaQuery } from '@/hooks'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  formatConnectionDuration,
  getConnectionPresentation,
  getConnectionRoute,
} from '../lib/connection-log-display'
import type { ConnectionLogItem } from '../types'
import {
  ConnectionEmptyState,
  ConnectionLiveIndicator,
  ConnectionRetryBadge,
  ConnectionTtftValue,
  ConnectionUserButton,
} from './connection-log-parts'
import { ConnectionLogsMobileList } from './connection-logs-mobile-card'
import { ConnectionLogsMobileGrid } from './connection-logs-mobile-grid'
import { ConnectionOutputTokens } from './connection-output-tokens'
import { ModelBadge } from './model-badge'

// The clock only feeds the elapsed/retry labels, which render to one decimal
// (0.1s). Ticking at 50ms rebuilt every visible row twenty times a second for
// digits nobody could read; 250ms keeps the labels accurate to a tenth while
// leaving the frame budget to the live bars.
const TICK_INTERVAL_MS = 250
const COLUMN_COUNT = 8
// The service keeps finished records for 60 seconds. Mirror the cutoff on the
// server-aligned clock so a completed row leaves on time instead of waiting for
// the next five-second snapshot.
const COMPLETED_RETENTION_MS = 60_000

/** Mobile density: two-line cards, one-line rows, or the request matrix. */
export type ConnectionViewMode = 'list' | 'compact' | 'grid'

function ConnectionRow(props: { item: ConnectionLogItem; now: number }) {
  const { t } = useTranslation()
  const presentation = getConnectionPresentation(props.item, props.now, t)
  const route = getConnectionRoute(props.item)

  return (
    <TableRow
      data-connection-request-id={props.item.request_id}
      className='connection-log-row animate-in fade-in-0 slide-in-from-right-4 duration-300 motion-reduce:animate-none'
    >
      <TableCell className='font-mono tabular-nums'>
        {formatTimestampToDate(props.item.started_at, 'milliseconds')}
      </TableCell>
      <TableCell>
        <div className='flex flex-col items-start gap-0.5'>
          <div className='flex h-5 items-center gap-2'>
            <StatusBadge
              label={t(presentation.meta.mainKey)}
              variant={presentation.meta.variant}
              copyable={false}
            />
            <ConnectionLiveIndicator
              item={props.item}
              tone={presentation.meta.variant}
            />
            <ConnectionRetryBadge count={props.item.retry_count} />
          </div>
          {/* The sub-state line is reserved even when empty so a connection
              changing state never moves the label or resizes the column. */}
          <span
            className={cn(
              'h-4 leading-4',
              presentation.retrying ? 'text-warning' : 'text-muted-foreground'
            )}
          >
            {presentation.subText}
          </span>
        </div>
      </TableCell>
      <TableCell
        className={cn(
          !props.item.username && 'text-muted-foreground font-mono'
        )}
      >
        <ConnectionUserButton item={props.item} />
      </TableCell>
      <TableCell>
        <div className='flex min-w-0 flex-col gap-0.5'>
          <ModelBadge modelName={props.item.model} className='max-w-full' />
          {route ? (
            <span className='text-muted-foreground truncate font-mono'>
              {route}
            </span>
          ) : null}
        </div>
      </TableCell>
      <TableCell className='font-mono font-medium tabular-nums'>
        {formatNumber(props.item.input_tokens)}
      </TableCell>
      <TableCell className='font-mono font-medium tabular-nums'>
        <ConnectionOutputTokens item={props.item} />
      </TableCell>
      <TableCell className='font-mono tabular-nums'>
        <ConnectionTtftValue item={props.item} />
      </TableCell>
      <TableCell className='font-mono tabular-nums'>
        {formatConnectionDuration(presentation.elapsed)}
      </TableCell>
    </TableRow>
  )
}

export function ConnectionLogsTable(props: {
  items: ConnectionLogItem[]
  /** Node clock minus client clock at fetch time, for live elapsed values. */
  clockOffset?: number | null
  /** Mobile presentation; desktop always renders the table. */
  viewMode?: ConnectionViewMode
}) {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const viewMode = props.viewMode ?? 'list'
  const scrollAreaRef = useRef<HTMLDivElement>(null)
  const previousRowTopsRef = useRef(new Map<string, number>())
  const previousPageKeyRef = useRef('')
  const previousLayoutRef = useRef('')
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 20,
  })
  const [clientNow, setClientNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = window.setInterval(
      () => setClientNow(Date.now()),
      TICK_INTERVAL_MS
    )
    return () => window.clearInterval(timer)
  }, [])

  const now =
    props.clockOffset == null ? clientNow : clientNow + props.clockOffset
  const retentionNow = Math.floor(now / 1000) * 1000
  const rows = useMemo(() => {
    const cutoff = retentionNow - COMPLETED_RETENTION_MS
    return [...props.items]
      .filter(
        (item) =>
          item.active || item.finished_at == null || item.finished_at >= cutoff
      )
      .reverse()
  }, [props.items, retentionNow])
  const table = useReactTable({
    data: rows,
    columns: [],
    state: { pagination },
    onPaginationChange: setPagination,
    getCoreRowModel: getCoreRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
  })
  const visibleRows = table.getRowModel().rows.map((row) => row.original)
  const visibleRowKey = visibleRows.map((item) => item.request_id).join('|')
  const pageKey = `${pagination.pageIndex}:${pagination.pageSize}`
  const pageCount = table.getPageCount()
  // Cards, compact rows and grid cells share the row id attribute, but a layout
  // switch replaces every element, so old positions must not drive an animation.
  const layoutKey = `${isMobile ? 'mobile' : 'desktop'}:${viewMode}`

  useEffect(() => {
    if (pagination.pageIndex >= Math.max(pageCount, 1)) {
      setPagination((current) => ({
        ...current,
        pageIndex: Math.max(pageCount - 1, 0),
      }))
    }
  }, [pageCount, pagination.pageIndex])

  // FLIP keeps existing rows in place, then animates them down as newcomers
  // slide in from the right.
  useLayoutEffect(() => {
    const scrollArea = scrollAreaRef.current
    if (!scrollArea) return

    const elements = [
      ...scrollArea.querySelectorAll<HTMLElement>(
        '[data-connection-request-id]'
      ),
    ]
    const nextPositions = new Map<string, number>()
    const movingElements: HTMLElement[] = []
    // Switching layout swaps the container, so old positions are meaningless:
    // reset them instead of animating a jump.
    if (previousLayoutRef.current !== layoutKey) {
      previousLayoutRef.current = layoutKey
      previousRowTopsRef.current.clear()
      previousPageKeyRef.current = pageKey
    }
    const pageChanged =
      previousPageKeyRef.current !== '' &&
      previousPageKeyRef.current !== pageKey
    previousPageKeyRef.current = pageKey
    if (pageChanged) scrollArea.scrollTop = 0
    const reduceMotion = window.matchMedia?.(
      '(prefers-reduced-motion: reduce)'
    ).matches

    for (const element of elements) {
      const requestID = element.dataset.connectionRequestId
      if (!requestID) continue
      const top = element.getBoundingClientRect().top
      nextPositions.set(requestID, top)
      const previousTop = previousRowTopsRef.current.get(requestID)
      element.style.transition = 'none'
      if (pageChanged) {
        element.style.transform = ''
        element.style.opacity = ''
      } else if (previousTop == null) {
        // Newcomers slide in from the right through the CSS enter animation.
        element.style.transform = ''
        element.style.opacity = ''
      } else {
        const offsetY = previousTop - top
        element.style.transform = `translateY(${offsetY}px)`
      }
      movingElements.push(element)
    }
    previousRowTopsRef.current = nextPositions

    if (pageChanged || reduceMotion || movingElements.length === 0) {
      for (const element of movingElements) {
        element.style.transition = ''
        element.style.transform = ''
        element.style.opacity = ''
      }
      return
    }

    void scrollArea.offsetHeight
    const animationFrame = window.requestAnimationFrame(() => {
      for (const element of movingElements) {
        element.style.transition =
          'transform 360ms cubic-bezier(0.2, 0.8, 0.2, 1), opacity 260ms ease'
        element.style.transform = ''
        element.style.opacity = ''
      }
    })
    const cleanupTimer = window.setTimeout(() => {
      for (const element of movingElements) element.style.transition = ''
    }, 400)

    return () => {
      window.cancelAnimationFrame(animationFrame)
      window.clearTimeout(cleanupTimer)
    }
  }, [layoutKey, pageKey, visibleRowKey])

  if (isMobile) {
    if (viewMode === 'grid') {
      return (
        <ConnectionLogsMobileGrid
          rows={rows}
          now={now}
          scrollAreaRef={scrollAreaRef}
        />
      )
    }

    return (
      <ConnectionLogsMobileList
        rows={rows}
        visibleRows={visibleRows}
        now={now}
        scrollAreaRef={scrollAreaRef}
        table={table}
        layout={viewMode}
      />
    )
  }

  return (
    <div className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border'>
      <div
        ref={scrollAreaRef}
        className='min-h-0 flex-1 overflow-x-auto overflow-y-auto overscroll-contain **:data-[slot=table-header]:bg-(--table-header-bg) **:data-[slot=table-header]:[--table-header-bg:var(--table-header)]'
      >
        {/* Fixed layout: live values (tokens, duration, sub-state) change every
            poll, and an auto-laid-out table reflows all columns whenever one
            of them changes width. */}
        <table
          data-slot='table'
          className='w-full min-w-[1120px] table-fixed text-[13px] tabular-nums [&_td]:text-[13px] [&_td_*]:text-[13px] [&_th]:text-[13px] [&_th_*]:text-[13px]'
        >
          <TableHeader className='sticky top-0 z-10'>
            <TableRow>
              <TableHead className='w-[14.5%]'>{t('Time')}</TableHead>
              <TableHead className='w-[19.5%]'>{t('Status')}</TableHead>
              <TableHead className='w-[13%]'>{t('User')}</TableHead>
              <TableHead className='w-[19%]'>{t('Model')}</TableHead>
              <TableHead className='w-[9%]'>{t('Input tokens')}</TableHead>
              <TableHead className='w-[10%]'>{t('Output tokens')}</TableHead>
              <TableHead className='w-[7.5%]'>{t('TTFT')}</TableHead>
              <TableHead className='w-[7.5%]'>{t('Duration')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={COLUMN_COUNT} className='h-40 p-0'>
                  <ConnectionEmptyState />
                </TableCell>
              </TableRow>
            ) : (
              visibleRows.map((item) => (
                <ConnectionRow key={item.request_id} item={item} now={now} />
              ))
            )}
          </TableBody>
        </table>
      </div>
      {rows.length > 0 ? (
        <div className='shrink-0 border-t px-2 py-1.5 sm:px-3'>
          <DataTablePagination table={table} />
        </div>
      ) : null}
    </div>
  )
}
