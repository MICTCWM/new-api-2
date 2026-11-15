import type { CSSProperties } from 'react'

import { textColorMap, type StatusVariant } from '@/components/status-badge'
import { cn } from '@/lib/utils'

// Non-stream requests never produce a cadence, so they get a different motion:
// vertical bars with highlights falling down them. Every bar runs its own speed
// and phase, so the drops stay visibly staggered instead of falling in a wave.
const BARS = [
  { id: 'tall', height: 12, duration: 0.52, delay: -0.06 },
  { id: 'short', height: 8, duration: 0.62, delay: -0.31 },
  { id: 'medium', height: 11, duration: 0.55, delay: -0.47 },
  { id: 'stub', height: 6, duration: 0.66, delay: -0.17 },
  { id: 'high', height: 10, duration: 0.58, delay: -0.39 },
  { id: 'low', height: 7, duration: 0.5, delay: -0.24 },
]

interface ConnectionDropMeterProps {
  /** Shared status tone, so the meter reads as part of the status label. */
  tone: StatusVariant
}

export function ConnectionDropMeter(props: ConnectionDropMeterProps) {
  return (
    <span
      data-slot='connection-drop-meter'
      className={cn(
        'connection-drop flex h-3 w-11 items-center justify-between',
        textColorMap[props.tone]
      )}
      aria-hidden='true'
    >
      {BARS.map((bar) => (
        <span
          key={bar.id}
          className='connection-drop-bar w-1'
          style={
            {
              height: `${bar.height}px`,
              '--drop-dur': `${bar.duration}s`,
              '--drop-delay': `${bar.delay}s`,
            } as CSSProperties
          }
        >
          <span className='connection-drop-highlight' />
        </span>
      ))}
    </span>
  )
}
