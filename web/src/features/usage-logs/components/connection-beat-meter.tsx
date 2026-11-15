import { useEffect, useRef } from 'react'

import { dotColorMap, type StatusVariant } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import type { ConnectionState } from '../types'

// One bar per tick. When the backend sends real 100ms cadence buckets, the
// strip is a true short history of upstream token bursts; without cadence it
// falls back to spreading each poll delta over the next interval.
const BAR_COUNT = 12
const TICK_MS = 50
/** The delta reported by one progress poll is spread over this interval. */
const REFILL_MS = 500
const BAR_TRACK_PX = 14
/** Tokens per bar that already draw full height (≈15 tokens/s). */
const FULL_BAR_TOKENS = 0.75
/**
 * Real 100ms buckets: 48 tokens in a bucket means a 480 tokens/s burst. A
 * lower cap (the old 6) saturated almost every active bucket at full height,
 * which made the strip read as binary instead of showing a rate gradation.
 */
const FULL_CADENCE_TOKENS = 48
const BAR_INDEXES = Array.from({ length: BAR_COUNT }, (_, index) => index)
/** One visual step every 50ms; five steps ease a new cadence window in 250ms. */
const CADENCE_EASE_TICKS = 5

function cadenceBarHeight(tokens: number): number {
  if (tokens <= 0) return 2
  // Logarithmic scaling keeps 1–4 token buckets distinguishable while still
  // leaving headroom for very fast streams; square-root alone compressed the
  // middle of the range once the cap moved up.
  const ratio = Math.min(tokens, FULL_CADENCE_TOKENS) / FULL_CADENCE_TOKENS
  const scaled =
    2 +
    (Math.log1p(ratio * FULL_CADENCE_TOKENS) /
      Math.log1p(FULL_CADENCE_TOKENS)) *
      (BAR_TRACK_PX - 2)
  return Math.max(2, Math.min(BAR_TRACK_PX, Math.round(scaled)))
}

/** Write the current heights without rounding away sub-pixel steps. */
function paintBarHeights(
  bars: Array<HTMLSpanElement | null>,
  heights: number[]
) {
  for (const index of BAR_INDEXES) {
    const bar = bars[index]
    if (!bar) continue
    bar.style.height = `${Math.round(heights[index] * 10) / 10}px`
  }
}

interface ConnectionBeatMeterProps {
  state: ConnectionState
  active: boolean
  /** Estimated output tokens from the progress poll; null before the first token. */
  tokens: number | null
  /** Real 100ms output-token buckets from the backend, oldest first. */
  cadence?: number[] | null
  /** Shared status tone, so the meter reads as part of the status label. */
  tone: StatusVariant
}

export function ConnectionBeatMeter(props: ConnectionBeatMeterProps) {
  const barsRef = useRef<Array<HTMLSpanElement | null>>([])
  const levelsRef = useRef<number[]>(Array.from({ length: BAR_COUNT }, () => 0))
  const tokensRef = useRef(props.tokens)
  tokensRef.current = props.tokens
  const lastTokensRef = useRef<number | null>(null)
  const pendingRef = useRef(0)
  const spendPerMSRef = useRef(0)
  // Cadence is painted locally: the last window is the target, and the bars
  // walk toward it in 50ms steps instead of jumping when the next poll lands.
  const cadenceTargetsRef = useRef<number[]>(
    Array.from({ length: BAR_COUNT }, () => 2)
  )
  const cadenceHeightsRef = useRef<number[]>(
    Array.from({ length: BAR_COUNT }, () => 2)
  )
  const cadenceStepsRef = useRef<number[]>(
    Array.from({ length: BAR_COUNT }, () => 0)
  )
  const cadenceReadyRef = useRef(false)

  const streaming = props.active && props.state === 'streaming'
  const waiting = props.active && !streaming
  const cadence = props.cadence ?? null
  const hasCadence = streaming && cadence != null && cadence.length > 0
  const cadenceKey = hasCadence ? cadence.slice(-BAR_COUNT).join(',') : ''

  // The backend publishes a time-aligned window of real 100ms buckets. That
  // window is the target silhouette; the 50ms tick below eases every bar from
  // its current height to the target, so a 250ms poll still reads as motion.
  useEffect(() => {
    if (!hasCadence || cadence == null) return
    const values = cadence.slice(-BAR_COUNT)
    const targets = Array.from({ length: BAR_COUNT }, (_, index) =>
      cadenceBarHeight(values[index] ?? 0)
    )
    cadenceTargetsRef.current = targets

    const current = cadenceHeightsRef.current
    if (!cadenceReadyRef.current) {
      // First real window: snap to it, then animate every later update.
      cadenceHeightsRef.current = [...targets]
      cadenceStepsRef.current = Array.from({ length: BAR_COUNT }, () => 0)
      cadenceReadyRef.current = true
      paintBarHeights(barsRef.current, cadenceHeightsRef.current)
      return
    }
    cadenceStepsRef.current = targets.map(
      (target, index) => (target - current[index]) / CADENCE_EASE_TICKS
    )
  }, [cadence, cadenceKey, hasCadence])

  // One 50ms step per tick: the five-step ease finishes in 250ms, matching the
  // progress-poll interval. New windows retarget the remaining distance.
  useEffect(() => {
    if (!hasCadence) {
      cadenceReadyRef.current = false
      cadenceHeightsRef.current = Array.from({ length: BAR_COUNT }, () => 2)
      cadenceStepsRef.current = Array.from({ length: BAR_COUNT }, () => 0)
      return
    }

    const tick = () => {
      const current = cadenceHeightsRef.current
      const targets = cadenceTargetsRef.current
      const steps = cadenceStepsRef.current
      for (const index of BAR_INDEXES) {
        const step = steps[index]
        if (step === 0) continue
        const target = targets[index]
        let next = current[index] + step
        if ((step > 0 && next >= target) || (step < 0 && next <= target)) {
          next = target
          steps[index] = 0
        }
        current[index] = next
        const bar = barsRef.current[index]
        if (bar) {
          bar.style.height = `${Math.round(next * 10) / 10}px`
        }
      }
    }

    const timer = window.setInterval(tick, TICK_MS)
    return () => window.clearInterval(timer)
  }, [hasCadence])

  // Polls only report a running total, so turn each delta into a rate the tick
  // loop can spend evenly instead of replaying the burst as a single spike.
  useEffect(() => {
    if (!streaming || hasCadence) {
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
  }, [props.tokens, streaming, hasCadence])

  useEffect(() => {
    if (!streaming || hasCadence) return

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
  }, [streaming, hasCadence])

  if (!props.active) return null

  return (
    <span
      data-slot='connection-beat-meter'
      data-scan={waiting ? '1' : '0'}
      className='connection-beat flex h-3.5 items-end gap-[2px]'
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
