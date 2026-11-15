import { useEffect, useRef } from 'react'

import { dotColorMap, type StatusVariant } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import type { ConnectionState } from '../types'

// One bar per tick, so the whole strip scrolls in 600ms: fast enough to read as
// motion rather than a slideshow. Each poll's token delta is spent over the
// next poll interval, which keeps the bars moving between polls.
const BAR_COUNT = 12
const TICK_MS = 50
/** The delta reported by one progress poll is spread over this interval. */
const REFILL_MS = 500
const BAR_TRACK_PX = 12
/** Tokens per bar that already draw full height (≈15 tokens/s). */
const FULL_BAR_TOKENS = 0.75
const BAR_INDEXES = Array.from({ length: BAR_COUNT }, (_, index) => index)

interface ConnectionBeatMeterProps {
  state: ConnectionState
  active: boolean
  /** Estimated output tokens from the progress poll; null before the first token. */
  tokens: number | null
  /** Shared status tone, so the meter reads as part of the status label. */
  tone: StatusVariant
}

export function ConnectionBeatMeter(props: ConnectionBeatMeterProps) {
  const barsRef = useRef<Array<HTMLSpanElement | null>>([])
  const levelsRef = useRef<number[]>(
    Array.from({ length: BAR_COUNT }, () => 0)
  )
  const tokensRef = useRef(props.tokens)
  tokensRef.current = props.tokens
  const lastTokensRef = useRef<number | null>(null)
  const pendingRef = useRef(0)
  const spendPerMSRef = useRef(0)

  const streaming = props.active && props.state === 'streaming'
  const waiting = props.active && !streaming

  // Polls only report a running total, so turn each delta into a rate the tick
  // loop can spend evenly instead of replaying the burst as a single spike.
  useEffect(() => {
    if (!streaming) {
      lastTokensRef.current = null
      pendingRef.current = 0
      spendPerMSRef.current = 0
      return
    }
    const tokens = tokensRef.current ?? 0
    if (lastTokensRef.current == null) {
      // The estimate published at TTFT is a baseline, not one tick of output.
      lastTokensRef.current = tokens
      return
    }
    pendingRef.current += Math.max(0, tokens - lastTokensRef.current)
    lastTokensRef.current = tokens
    spendPerMSRef.current = pendingRef.current / REFILL_MS
  }, [props.tokens, streaming])

  useEffect(() => {
    if (!streaming) return

    const tick = () => {
      const spent = Math.min(
        pendingRef.current,
        spendPerMSRef.current * TICK_MS
      )
      pendingRef.current -= spent
      const levels = [...levelsRef.current.slice(1), spent]
      levelsRef.current = levels
      const peak = Math.max(FULL_BAR_TOKENS, ...levels)
      for (const index of BAR_INDEXES) {
        const bar = barsRef.current[index]
        if (!bar) continue
        const height = Math.max(
          2,
          Math.round((levels[index] / peak) * BAR_TRACK_PX)
        )
        bar.style.height = `${height}px`
      }
    }

    const timer = window.setInterval(tick, TICK_MS)
    return () => window.clearInterval(timer)
  }, [streaming])

  if (!props.active) return null

  return (
    <span
      data-slot='connection-beat-meter'
      data-scan={waiting ? '1' : '0'}
      className='connection-beat flex h-3 items-end gap-[2px]'
      aria-hidden='true'
    >
      {BAR_INDEXES.map((index) => (
        <span
          key={index}
          ref={(node) => {
            barsRef.current[index] = node
            node?.style.setProperty('--beat-i', String(index))
          }}
          className={cn(
            'connection-beat-bar w-0.5 rounded-full',
            dotColorMap[props.tone]
          )}
        />
      ))}
    </span>
  )
}
