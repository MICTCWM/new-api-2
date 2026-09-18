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
import { useEffect, useMemo, useState, type ComponentType } from 'react'
import { useTranslation } from 'react-i18next'

import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

import {
  buildSessionOccupancySpec,
  clampSessionOccupancy,
} from '../lib/session-occupancy'

type OccupancyChartStatus = 'loading' | 'error' | 'empty' | 'ready'

interface SessionOccupancyChartProps {
  activeSessions?: number
  capacity?: number
  status: OccupancyChartStatus
}

type VChartComponent = ComponentType<{
  spec: Record<string, unknown>
  option: typeof VCHART_OPTION
}>

let vchartPromise: Promise<VChartComponent> | null = null

function loadVChart(): Promise<VChartComponent> {
  if (!vchartPromise) {
    vchartPromise = import('@visactor/react-vchart').then(
      (module) => module.VChart as VChartComponent
    )
  }
  return vchartPromise
}

function OccupancyPlaceholder(props: { label: string }) {
  return (
    <div
      className='text-muted-foreground flex size-16 shrink-0 items-center justify-center text-[10px]'
      role='img'
      aria-label={props.label}
    >
      —
    </div>
  )
}

export function SessionOccupancyChart(props: SessionOccupancyChartProps) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const [VChart, setVChart] = useState<VChartComponent | null>(null)
  const occupancy = clampSessionOccupancy(
    props.activeSessions ?? 0,
    props.capacity ?? 0
  )
  const activeLabel = t('Active sessions')
  const availableLabel = t('Available')
  const spec = useMemo(() => {
    const occupancySpec = buildSessionOccupancySpec({
      activeSessions: occupancy.active,
      capacity: occupancy.capacity,
      activeLabel,
      availableLabel,
    })
    if (!occupancySpec) return null
    return {
      ...occupancySpec,
      theme: resolvedTheme === 'dark' ? 'dark' : 'light',
      background: 'transparent',
    }
  }, [
    occupancy.active,
    occupancy.capacity,
    activeLabel,
    availableLabel,
    resolvedTheme,
  ])

  useEffect(() => {
    let cancelled = false
    loadVChart()
      .then((component) => {
        if (!cancelled) setVChart(() => component)
      })
      .catch(() => {
        if (!cancelled) setVChart(null)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const ariaLabel = t('Current sessions: {{count}} / {{limit}}', {
    count: occupancy.active,
    limit: occupancy.capacity,
  })

  if (props.status === 'loading') {
    return <OccupancyPlaceholder label={t('Session occupancy')} />
  }
  if (props.status === 'error') {
    return (
      <OccupancyPlaceholder label={t('Failed to load session occupancy')} />
    )
  }
  if (props.status === 'empty' || !spec) {
    return <OccupancyPlaceholder label={t('No session occupancy data')} />
  }
  if (!themeReady || !VChart) {
    return <OccupancyPlaceholder label={t('Session occupancy')} />
  }

  return (
    <div
      className='relative size-16 shrink-0'
      role='img'
      aria-label={ariaLabel}
      title={`${t('Session occupancy')}: ${occupancy.active} / ${occupancy.capacity}`}
    >
      <VChart spec={spec} option={VCHART_OPTION} />
      <span className='pointer-events-none absolute inset-0 flex items-center justify-center text-[10px] font-medium tabular-nums'>
        {occupancy.active}/{occupancy.capacity}
      </span>
    </div>
  )
}
