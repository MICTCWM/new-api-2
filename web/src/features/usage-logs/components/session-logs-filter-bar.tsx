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
import { useIsFetching, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import type { Table } from '@tanstack/react-table'
import { Eye, EyeOff } from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import {
  SESSION_LOG_EVENT_ALL,
  SESSION_LOG_EVENT_FILTERS,
} from '../constants'
import { buildSearchParams } from '../lib/filter'
import { getDefaultTimeRange } from '../lib/utils'
import type { SessionLogFilters } from '../types'
import { CompactDateTimeRangePicker } from './compact-date-time-range-picker'
import {
  LogsFilterField,
  LogsFilterInput,
  LogsFilterToolbar,
} from './logs-filter-toolbar'
import { useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

type SessionEventValue = (typeof SESSION_LOG_EVENT_FILTERS)[number]['value']
const sessionEventValueSet = new Set<string>(
  SESSION_LOG_EVENT_FILTERS.map((item) => item.value)
)

type SessionLogDraft = {
  sourceKey: string
  filters: SessionLogFilters
}

function isSessionEventValue(value: string): value is SessionEventValue {
  return sessionEventValueSet.has(value)
}

function getSessionEventValue(value: unknown): SessionEventValue {
  return typeof value === 'string' && isSessionEventValue(value)
    ? value
    : SESSION_LOG_EVENT_ALL
}

function buildSearchSourceKey(values: {
  startTime?: unknown
  endTime?: unknown
  channel?: unknown
  model?: unknown
  group?: unknown
  username?: unknown
  event?: unknown
}) {
  return [
    values.startTime,
    values.endTime,
    values.channel,
    values.model,
    values.group,
    values.username,
    values.event,
  ]
    .map((value) => String(value ?? ''))
    .join('\u001f')
}

interface SessionLogsFilterBarProps<TData> {
  table: Table<TData>
}

export function SessionLogsFilterBar<TData>(
  props: SessionLogsFilterBarProps<TData>
) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const searchParams = route.useSearch()
  const { sensitiveVisible, setSensitiveVisible } = useUsageLogsContext()
  const fetchingLogs = useIsFetching({ queryKey: ['logs'] })
  const sensitiveType = sensitiveVisible ? 'text' : 'password'

  const searchState = useMemo<SessionLogDraft>(() => {
    const { start, end } = getDefaultTimeRange()
    const sourceValues = {
      startTime: searchParams.startTime,
      endTime: searchParams.endTime,
      channel: searchParams.channel,
      model: searchParams.model,
      group: searchParams.group,
      username: searchParams.username,
      event: searchParams.event,
    }
    const filters: SessionLogFilters = {
      startTime: searchParams.startTime
        ? new Date(searchParams.startTime)
        : start,
      endTime: searchParams.endTime ? new Date(searchParams.endTime) : end,
      channel: searchParams.channel || undefined,
      model: searchParams.model || undefined,
      group: searchParams.group || undefined,
      username: searchParams.username || undefined,
      event: getSessionEventValue(searchParams.event) || undefined,
    }
    return {
      sourceKey: buildSearchSourceKey(sourceValues),
      filters,
    }
  }, [
    searchParams.startTime,
    searchParams.endTime,
    searchParams.channel,
    searchParams.model,
    searchParams.group,
    searchParams.username,
    searchParams.event,
  ])
  const [draft, setDraft] = useState<SessionLogDraft>(() => searchState)
  const activeDraft =
    draft.sourceKey === searchState.sourceKey ? draft : searchState
  const filters = activeDraft.filters
  const eventValue = getSessionEventValue(filters.event)

  const handleChange = useCallback(
    (field: keyof SessionLogFilters, value: Date | string | undefined) => {
      setDraft((current) => {
        const base =
          current.sourceKey === searchState.sourceKey ? current : searchState
        return {
          sourceKey: searchState.sourceKey,
          filters: { ...base.filters, [field]: value },
        }
      })
    },
    [searchState]
  )

  const handleApply = useCallback(() => {
    const filterParams = buildSearchParams(filters, 'session')
    navigate({
      to: '/usage-logs/$section',
      params: { section: 'session' },
      search: {
        ...filterParams,
        page: 1,
      },
    })
    queryClient.invalidateQueries({ queryKey: ['logs'] })
  }, [filters, navigate, queryClient])

  const handleReset = useCallback(() => {
    const { start, end } = getDefaultTimeRange()
    const resetFilters: SessionLogFilters = { startTime: start, endTime: end }
    setDraft({
      sourceKey: searchState.sourceKey,
      filters: resetFilters,
    })
    navigate({
      to: '/usage-logs/$section',
      params: { section: 'session' },
      search: {
        page: 1,
        startTime: start.getTime(),
        endTime: end.getTime(),
      },
    })
    queryClient.invalidateQueries({ queryKey: ['logs'] })
  }, [navigate, queryClient, searchState.sourceKey])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') handleApply()
    },
    [handleApply]
  )

  const eventItems = useMemo(
    () =>
      SESSION_LOG_EVENT_FILTERS.map((item) => ({
        value: item.value || 'all',
        label: t(item.label),
      })),
    [t]
  )
  const eventLabel =
    eventItems.find((item) => item.value === (eventValue || 'all'))?.label ??
    t('All Events')
  const hasActiveFilters =
    !!filters.model ||
    !!filters.group ||
    !!filters.username ||
    !!filters.channel ||
    eventValue !== SESSION_LOG_EVENT_ALL

  const dateRangeFilter = (
    <LogsFilterField wide>
      <CompactDateTimeRangePicker
        start={filters.startTime}
        end={filters.endTime}
        onChange={({ start, end }) => {
          handleChange('startTime', start)
          handleChange('endTime', end)
        }}
      />
    </LogsFilterField>
  )
  const eventFilter = (
    <LogsFilterField>
      <Select
        items={eventItems}
        value={eventValue || 'all'}
        onValueChange={(value) => {
          const nextEvent =
            value !== null && value !== 'all' && isSessionEventValue(value)
              ? value
              : SESSION_LOG_EVENT_ALL
          handleChange('event', nextEvent || undefined)
        }}
      >
        <SelectTrigger>
          <SelectValue>{eventLabel}</SelectValue>
        </SelectTrigger>
        <SelectContent alignItemWithTrigger={false}>
          <SelectGroup>
            {SESSION_LOG_EVENT_FILTERS.map((item) => (
              <SelectItem key={item.value || 'all'} value={item.value || 'all'}>
                {t(item.label)}
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
    </LogsFilterField>
  )
  const modelFilter = (
    <LogsFilterField>
      <LogsFilterInput
        placeholder={t('Model Name')}
        value={filters.model || ''}
        onChange={(e) => handleChange('model', e.target.value)}
        onKeyDown={handleKeyDown}
      />
    </LogsFilterField>
  )
  const groupFilter = (
    <LogsFilterField>
      <LogsFilterInput
        placeholder={t('Group')}
        type={sensitiveType}
        value={filters.group || ''}
        onChange={(e) => handleChange('group', e.target.value)}
        onKeyDown={handleKeyDown}
      />
    </LogsFilterField>
  )
  const advancedFilters = (
    <>
      <LogsFilterField>
        <LogsFilterInput
          placeholder={t('Username')}
          type={sensitiveType}
          value={filters.username || ''}
          onChange={(e) => handleChange('username', e.target.value)}
          onKeyDown={handleKeyDown}
        />
      </LogsFilterField>
      <LogsFilterField>
        <LogsFilterInput
          placeholder={t('Channel ID')}
          value={filters.channel || ''}
          onChange={(e) => handleChange('channel', e.target.value)}
          onKeyDown={handleKeyDown}
        />
      </LogsFilterField>
    </>
  )
  const sensitiveToggle = (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant='ghost'
            size='icon'
            onClick={() => setSensitiveVisible(!sensitiveVisible)}
            aria-label={sensitiveVisible ? t('Hide') : t('Show')}
            className='text-muted-foreground hover:text-foreground size-7'
          />
        }
      >
        {sensitiveVisible ? <Eye /> : <EyeOff />}
      </TooltipTrigger>
      <TooltipContent>
        {sensitiveVisible ? t('Hide') : t('Show')}
      </TooltipContent>
    </Tooltip>
  )

  return (
    <LogsFilterToolbar
      table={props.table}
      actionStart={sensitiveToggle}
      primaryFilters={
        <>
          {dateRangeFilter}
          {eventFilter}
          {modelFilter}
          {groupFilter}
        </>
      }
      advancedFilters={advancedFilters}
      mobilePinnedFilters={dateRangeFilter}
      mobileFilters={
        <>
          {eventFilter}
          {modelFilter}
          {groupFilter}
          {advancedFilters}
        </>
      }
      mobileFilterCount={
        [
          eventValue !== SESSION_LOG_EVENT_ALL,
          filters.model,
          filters.group,
          filters.username,
          filters.channel,
        ].filter(Boolean).length
      }
      hasAdvancedActiveFilters={!!filters.username || !!filters.channel}
      advancedFilterCount={[filters.username, filters.channel].filter(Boolean)
        .length}
      hasActiveFilters={hasActiveFilters}
      onSearch={handleApply}
      searchLoading={fetchingLogs > 0}
      onReset={handleReset}
    />
  )
}
