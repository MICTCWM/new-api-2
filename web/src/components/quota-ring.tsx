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
import { cn } from '@/lib/utils'

export interface QuotaRingProps {
  label: string
  used: number
  cap: number
  percent: number
  resetText?: string
  className?: string
}

const RADIUS = 16
const CIRCUMFERENCE = 2 * Math.PI * RADIUS

/** Shared quota thresholds used by the account pool and channel table gauges. */
export const QUOTA_RING_WARNING_PERCENT = 50
export const QUOTA_RING_CRITICAL_PERCENT = 90

function formatQuotaValue(value: number): string {
  if (!Number.isFinite(value)) return '-'
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits: value >= 100 ? 0 : 1,
    notation: value >= 1_000_000 ? 'compact' : 'standard',
  }).format(value)
}

function clampPercent(percent: number): number {
  if (!Number.isFinite(percent)) return 0
  return Math.min(100, Math.max(0, percent))
}

/**
 * Circular quota gauge used by account pools. Shows the used/cap values, the
 * percentage, and an optional reset countdown below a color-coded ring.
 */
export function QuotaRing(props: QuotaRingProps) {
  const percent = clampPercent(props.percent)
  let color = 'text-green-500'
  if (percent >= QUOTA_RING_CRITICAL_PERCENT) {
    color = 'text-red-500'
  } else if (percent >= QUOTA_RING_WARNING_PERCENT) {
    color = 'text-yellow-500'
  }

  return (
    <div
      className={cn(
        'flex min-w-20 flex-col items-center gap-1 text-center',
        props.className
      )}
    >
      <div
        className={cn('relative size-14', color)}
        role='img'
        aria-label={`${props.label}: ${formatQuotaValue(props.used)} / ${formatQuotaValue(props.cap)} (${Math.round(percent)}%)`}
      >
        <svg className='size-14 -rotate-90' viewBox='0 0 40 40'>
          <circle
            cx='20'
            cy='20'
            r={RADIUS}
            fill='none'
            className='text-muted stroke-current'
            strokeWidth='4'
          />
          <circle
            cx='20'
            cy='20'
            r={RADIUS}
            fill='none'
            className='stroke-current transition-all'
            strokeWidth='4'
            strokeLinecap='round'
            strokeDasharray={CIRCUMFERENCE}
            strokeDashoffset={CIRCUMFERENCE * (1 - percent / 100)}
          />
        </svg>
        <span className='absolute inset-0 flex items-center justify-center text-[11px] font-semibold tabular-nums'>
          {Math.round(percent)}%
        </span>
      </div>
      <span className='text-xs font-medium'>{props.label}</span>
      <span className='text-muted-foreground text-[11px] tabular-nums'>
        {formatQuotaValue(props.used)} / {formatQuotaValue(props.cap)}
      </span>
      {props.resetText && (
        <span className='text-muted-foreground/80 text-[10px] tabular-nums'>
          {props.resetText}
        </span>
      )}
    </div>
  )
}
