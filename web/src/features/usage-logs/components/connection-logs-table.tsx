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
import {
  getCoreRowModel,
  getPaginationRowModel,
  useReactTable,
  type PaginationState,
} from '@tanstack/react-table'
import { Activity } from 'lucide-react'
import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePagination } from '@/components/data-table'
import {
  StatusBadge,
  textColorMap,
  type StatusVariant,
} from '@/components/status-badge'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getFirstResponseTimeColor } from '../lib/format'
import type { ConnectionLogItem, ConnectionState } from '../types'
import { ModelBadge } from './model-badge'
import { useUsageLogsContext } from './usage-logs-provider'

const EMPTY_VALUE = '--'
const TICK_INTERVAL_MS = 50
/** Time constant of the output-token catch-up: smaller = snappier counting. */
const OUTPUT_COUNT_CATCHUP_MS = 220
/** Guards the easing against long frame gaps (background tab, heavy render). */
const OUTPUT_COUNT_MAX_FRAME_MS = 250
const COLUMN_COUNT = 8

function formatConnectionDuration(milliseconds: number): string {
  const seconds = Math.max(0, milliseconds) / 1000
  if (seconds < 60) return `${seconds.toFixed(1)}s`
  const roundedSeconds = Math.round(seconds)
  return `${Math.floor(roundedSeconds / 60)}m ${roundedSeconds % 60}s`
}

interface ConnectionStateMeta {
  /** Top-level state: every open connection reads 正在连接. */
  mainKey: string
  /** Sub-state shown only for streaming requests while they are open. */
  subKey?: string
  variant: StatusVariant
  pulse: boolean
}

const CONNECTION_STATE_META: Record<ConnectionState, ConnectionStateMeta> = {
  waiting_first_token: {
    mainKey: 'Connecting',
    subKey: 'Waiting for first token',
    variant: 'warning',
    pulse: true,
  },
  streaming: {
    mainKey: 'Connecting',
    subKey: 'Streaming output',
    variant: 'info',
    pulse: true,
  },
  // Non-stream requests only show the top-level 正在连接 state.
  waiting_response: {
    mainKey: 'Connecting',
    variant: 'warning',
    pulse: true,
  },
  completed: {
    mainKey: 'Connection completed',
    variant: 'success',
    pulse: false,
  },
}

function ConnectionUserCell(props: { item: ConnectionLogItem }) {
  const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
    useUsageLogsContext()

  if (!props.item.username) {
    return (
      <TableCell className='text-muted-foreground font-mono'>
        #{props.item.user_id}
      </TableCell>
    )
  }

  return (
    <TableCell>
      <button
        type='button'
        className='flex items-center gap-1.5 text-left'
        onClick={(event) => {
          event.stopPropagation()
          setSelectedUserId(props.item.user_id)
          setUserInfoDialogOpen(true)
        }}
      >
        <Avatar className='ring-border/60 size-6 ring-1 max-sm:hidden'>
          <AvatarFallback
            className={cn(
              'font-semibold',
              !sensitiveVisible && 'bg-muted text-muted-foreground'
            )}
            style={
              sensitiveVisible
                ? getUserAvatarStyle(props.item.username)
                : undefined
            }
          >
            {sensitiveVisible
              ? getUserAvatarFallback(props.item.username)
              : '•'}
          </AvatarFallback>
        </Avatar>
        <TooltipProvider delay={300}>
          <Tooltip>
            <TooltipTrigger
              render={
                <span className='text-muted-foreground max-w-[100px] truncate hover:underline' />
              }
            >
              {sensitiveVisible ? props.item.username : '••••'}
            </TooltipTrigger>
            {sensitiveVisible && props.item.username.length > 12 && (
              <TooltipContent side='top'>{props.item.username}</TooltipContent>
            )}
          </Tooltip>
        </TooltipProvider>
      </button>
    </TableCell>
  )
}

function AnimatedOutputTokens(props: { value: number; active: boolean }) {
  const [displayValue, setDisplayValue] = useState(props.value)
  const displayValueRef = useRef(props.value)
  const targetRef = useRef(props.value)
  targetRef.current = props.value

  useEffect(() => {
    const instant =
      !props.active ||
      (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false)
    if (instant) {
      displayValueRef.current = props.value
      setDisplayValue(props.value)
      return
    }

    // Chase the newest upstream value instead of replaying a fixed-length
    // tween: each poll only adds a small gap, so the counter keeps moving
    // instead of jumping ahead and freezing until the next poll.
    let animationFrame = 0
    let lastFrameAt = performance.now()
    const step = (now: number) => {
      const elapsed = Math.min(now - lastFrameAt, OUTPUT_COUNT_MAX_FRAME_MS)
      lastFrameAt = now
      const target = targetRef.current
      const current = displayValueRef.current
      if (current !== target) {
        const gap = target - current
        let delta = gap * (1 - Math.exp(-elapsed / OUTPUT_COUNT_CATCHUP_MS))
        if (Math.abs(delta) < 1) delta = gap > 0 ? 1 : -1
        const next =
          gap > 0
            ? Math.min(current + delta, target)
            : Math.max(current + delta, target)
        displayValueRef.current = next
        setDisplayValue(Math.round(next))
      }
      animationFrame = requestAnimationFrame(step)
    }

    animationFrame = requestAnimationFrame(step)
    return () => cancelAnimationFrame(animationFrame)
  }, [props.active, props.value])

  return <>{formatNumber(displayValue)}</>
}

function ConnectionRow(props: { item: ConnectionLogItem; now: number }) {
  const { t } = useTranslation()
  const meta = CONNECTION_STATE_META[props.item.state]
  const elapsed = props.item.active
    ? Math.max(0, props.now - props.item.started_at)
    : props.item.duration_ms
  const route = [
    props.item.group,
    props.item.channel_id > 0 ? `#${props.item.channel_id}` : '',
  ]
    .filter((segment) => segment !== '')
    .join(' · ')

  // TTFT belongs to streaming requests only, and reads -- until the first token.
  let ttftCell: ReactNode = null
  if (props.item.stream) {
    ttftCell = <span className='text-muted-foreground'>{EMPTY_VALUE}</span>
    if (props.item.ttft_ms != null) {
      const ttftSeconds = props.item.ttft_ms / 1000
      ttftCell = (
        <span className={textColorMap[getFirstResponseTimeColor(ttftSeconds)]}>
          {formatConnectionDuration(props.item.ttft_ms)}
        </span>
      )
    }
  }

  return (
    <TableRow
      data-connection-request-id={props.item.request_id}
      className='connection-log-row animate-in fade-in-0 slide-in-from-right-4 duration-300'
    >
      <TableCell className='font-mono tabular-nums'>
        {formatTimestampToDate(props.item.started_at, 'milliseconds')}
      </TableCell>
      <TableCell>
        <div className='flex flex-col items-start gap-0.5'>
          <StatusBadge
            label={t(meta.mainKey)}
            variant={meta.variant}
            pulse={meta.pulse}
            copyable={false}
          />
          {meta.subKey ? (
            <span className='text-muted-foreground'>{t(meta.subKey)}</span>
          ) : null}
        </div>
      </TableCell>
      <ConnectionUserCell item={props.item} />
      <TableCell>
        <div className='flex max-w-[240px] flex-col gap-0.5'>
          <ModelBadge modelName={props.item.model} />
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
        {props.item.output_tokens == null ? (
          <span className='text-muted-foreground'>{EMPTY_VALUE}</span>
        ) : (
          <AnimatedOutputTokens
            value={props.item.output_tokens}
            active={props.item.active && props.item.stream}
          />
        )}
      </TableCell>
      <TableCell className='font-mono tabular-nums'>{ttftCell}</TableCell>
      <TableCell className='font-mono tabular-nums'>
        {formatConnectionDuration(elapsed)}
      </TableCell>
    </TableRow>
  )
}

export function ConnectionLogsTable(props: {
  items: ConnectionLogItem[]
  /** Node clock minus client clock at fetch time, for live elapsed values. */
  clockOffset?: number | null
}) {
  const { t } = useTranslation()
  const scrollAreaRef = useRef<HTMLDivElement>(null)
  const previousRowTopsRef = useRef(new Map<string, number>())
  const previousPageKeyRef = useRef('')
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

  const rows = useMemo(() => [...props.items].reverse(), [props.items])
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
      ...scrollArea.querySelectorAll<HTMLTableRowElement>(
        '[data-connection-request-id]'
      ),
    ]
    const nextPositions = new Map<string, number>()
    const movingElements: HTMLTableRowElement[] = []
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
  }, [pageKey, visibleRowKey])

  const now =
    props.clockOffset == null ? clientNow : clientNow + props.clockOffset

  return (
    <div className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border'>
      <div
        ref={scrollAreaRef}
        className='min-h-0 flex-1 overflow-x-auto overflow-y-auto overscroll-contain **:data-[slot=table-header]:bg-(--table-header-bg) **:data-[slot=table-header]:[--table-header-bg:var(--table-header)]'
      >
        <table
          data-slot='table'
          className='w-full text-[13px] tabular-nums [&_td]:text-[13px] [&_td_*]:text-[13px] [&_th]:text-[13px] [&_th_*]:text-[13px]'
        >
          <TableHeader className='sticky top-0 z-10'>
            <TableRow>
              <TableHead>{t('Time')}</TableHead>
              <TableHead>{t('Status')}</TableHead>
              <TableHead>{t('User')}</TableHead>
              <TableHead>{t('Model')}</TableHead>
              <TableHead>{t('Input tokens')}</TableHead>
              <TableHead>{t('Output tokens')}</TableHead>
              <TableHead>{t('TTFT')}</TableHead>
              <TableHead>{t('Duration')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={COLUMN_COUNT} className='h-40 p-0'>
                  <Empty>
                    <EmptyHeader>
                      <EmptyMedia variant='icon'>
                        <Activity className='size-6' aria-hidden='true' />
                      </EmptyMedia>
                      <EmptyTitle>{t('No connections right now')}</EmptyTitle>
                    </EmptyHeader>
                  </Empty>
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
