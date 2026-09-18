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
import type { ColumnDef } from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { SESSION_LOG_EVENT_FILTERS } from '../../constants'
import type { UsageLog } from '../../data/schema'
import {
  formatModelName,
  parseLogOther,
  renderAuditContent,
} from '../../lib/format'
import { DetailsDialog } from '../dialogs/details-dialog'
import { ModelBadge } from '../model-badge'
import { useUsageLogsContext } from '../usage-logs-provider'

function sessionEventLabel(event: string): string {
  return (
    SESSION_LOG_EVENT_FILTERS.find((item) => item.value === event)?.label ??
    event
  )
}

function readSessionField(
  other: ReturnType<typeof parseLogOther>,
  key: string
): string | number | undefined {
  const fromParams = other?.op?.params?.[key]
  if (typeof fromParams === 'string' && fromParams !== '') return fromParams
  if (typeof fromParams === 'number' && Number.isFinite(fromParams)) {
    return fromParams
  }
  const adminInfo = other?.admin_info as Record<string, unknown> | undefined
  const fromAdmin = adminInfo?.[key]
  if (typeof fromAdmin === 'string' && fromAdmin !== '') return fromAdmin
  if (typeof fromAdmin === 'number' && Number.isFinite(fromAdmin)) {
    return fromAdmin
  }
  return undefined
}

export function useSessionLogsColumns(): ColumnDef<UsageLog>[] {
  const { t } = useTranslation()

  return [
    {
      accessorKey: 'created_at',
      header: t('Time'),
      cell: ({ row }) => {
        const timestamp = row.getValue('created_at') as number
        const other = parseLogOther(row.original.other)
        const eventToken = String(
          readSessionField(other, 'event') ??
            other?.op?.action?.replace(/^opencode\.session\./, '') ??
            ''
        )
        const eventKey = sessionEventLabel(eventToken)

        return (
          <div className='flex min-w-0 flex-col gap-0.5'>
            <span className='truncate font-mono text-xs tabular-nums'>
              {formatTimestampToDate(timestamp)}
            </span>
            {eventToken !== '' && (
              <StatusBadge
                label={t(eventKey)}
                variant='indigo'
                size='sm'
                copyable={false}
                className='-ml-1.5 !text-xs [&_span]:!text-xs'
              />
            )}
          </div>
        )
      },
      enableHiding: false,
      size: 180,
    },
    {
      id: 'session_id',
      header: t('Session ID'),
      accessorFn: (row) =>
        readSessionField(parseLogOther(row.other), 'session_id'),
      cell: ({ row }) => {
        const { sensitiveVisible } = useUsageLogsContext()
        const sessionId = readSessionField(
          parseLogOther(row.original.other),
          'session_id'
        )
        if (sessionId == null) return null
        const display = sensitiveVisible ? String(sessionId) : '••••'
        return (
          <StatusBadge
            label={display}
            copyText={sensitiveVisible ? String(sessionId) : undefined}
            size='sm'
            showDot={false}
            className='max-w-[220px] font-mono'
          />
        )
      },
      size: 220,
    },
    {
      accessorKey: 'username',
      header: t('User'),
      cell: function UserCell({ row }) {
        const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
          useUsageLogsContext()
        const log = row.original
        if (!log.username) return null

        return (
          <button
            type='button'
            className='flex min-w-0 items-center gap-2 text-left'
            onClick={() => {
              setSelectedUserId(log.user_id)
              setUserInfoDialogOpen(true)
            }}
          >
            <Avatar className='size-6'>
              <AvatarFallback
                style={getUserAvatarStyle(log.username)}
                className='text-[10px]'
              >
                {getUserAvatarFallback(log.username)}
              </AvatarFallback>
            </Avatar>
            <span className='truncate text-sm'>
              {sensitiveVisible ? log.username : '••••'}
            </span>
          </button>
        )
      },
      size: 140,
    },
    {
      id: 'channel',
      header: t('Channel'),
      accessorFn: (row) => row.channel,
      cell: ({ row }) => {
        const log = row.original
        const other = parseLogOther(log.other)
        const fromChannelId = readSessionField(other, 'from_channel_id')
        const toChannelId = readSessionField(other, 'to_channel_id')
        const channelId =
          readSessionField(other, 'channel_id') ||
          (log.channel > 0 ? log.channel : undefined)

        if (fromChannelId != null && toChannelId != null) {
          return (
            <span className='font-mono text-xs tabular-nums'>
              {fromChannelId} → {toChannelId}
            </span>
          )
        }
        if (channelId == null) return null

        return (
          <div className='flex max-w-[160px] flex-col gap-0.5'>
            <StatusBadge
              label={`#${channelId}`}
              autoColor={String(channelId)}
              copyText={String(channelId)}
              size='sm'
              showDot={false}
              className='font-mono'
            />
            {log.channel_name && (
              <span className='text-muted-foreground/70 truncate text-xs'>
                {log.channel_name}
              </span>
            )}
          </div>
        )
      },
      size: 160,
    },
    {
      accessorKey: 'model_name',
      header: t('Model'),
      cell: ({ row }) => {
        const log = row.original
        if (!log.model_name) return null
        const modelInfo = formatModelName(log)
        return (
          <ModelBadge
            modelName={modelInfo.name}
            actualModel={modelInfo.actualModel}
          />
        )
      },
      size: 160,
    },
    {
      accessorKey: 'group',
      header: t('Group'),
      cell: ({ row }) => {
        const { sensitiveVisible } = useUsageLogsContext()
        const group = row.original.group
        if (!group) return null
        return (
          <GroupBadge
            group={group}
            label={sensitiveVisible ? undefined : '••••'}
            type='text'
            size='sm'
          />
        )
      },
      size: 120,
    },
    {
      accessorKey: 'content',
      header: t('Details'),
      cell: function DetailsCell({ row }) {
        const [dialogOpen, setDialogOpen] = useState(false)
        const log = row.original
        const other = parseLogOther(log.other)
        const detailText = renderAuditContent(
          other,
          t,
          log.channel_name ? { channel_name: log.channel_name } : undefined
        )

        return (
          <>
            <button
              type='button'
              className='group flex max-w-[260px] items-center gap-1 text-left text-xs'
              onClick={() => setDialogOpen(true)}
              title={t('Click to view full details')}
            >
              <span
                className={cn(
                  'truncate leading-snug group-hover:underline',
                  detailText ? 'text-foreground' : 'text-muted-foreground/40'
                )}
              >
                {detailText || '—'}
              </span>
            </button>
            <DetailsDialog
              log={log}
              isAdmin
              open={dialogOpen}
              onOpenChange={setDialogOpen}
            />
          </>
        )
      },
      size: 260,
    },
  ]
}
