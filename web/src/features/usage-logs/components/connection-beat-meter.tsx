import { useEffect, useMemo, useRef } from 'react'

import { dotColorMap, type StatusVariant } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import { onFrame } from '../lib/frame-loop'
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
/**
 * Easing time constant. It has to stay close to the progress-poll interval
 * (150ms): a shorter constant reaches the target in two frames and then sits
 * still until the next poll, and that "move, freeze, move" rhythm is what read
 * as stutter in rc.88. Matching the interval keeps the strip moving
 * continuously between updates while staying far more responsive than rc.87's
 * 260ms constant against a 250ms poll.
 */
const CADENCE_EASE_MS = 160
/** A bar this close to its target is settled; the frame loop can stop. */
const SETTLE_EPSILON_PX = 0.05
const BAR_INDEXES = Array.from({ length: BAR_COUNT }, (_, index) => index)

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
  return Math.max(2, Math.min(BAR_TRACK_PX, scaled))
}

/**
 * Move a bar on the compositor, not in layout. The span keeps its 14px box and
 * scales from the bottom; height animation would relayout every bar on every
 * row, which is what made a full mobile list stutter. Heights are written as
 * sub-pixel scale factors so the easing is visible at every frame instead of
 * quantising into twelve 1px steps.
 */
function paintBarHeight(bar: HTMLSpanElement, height: number) {
  const clamped = Math.max(2, Math.min(BAR_TRACK_PX, height))
  const scale = clamped / BAR_TRACK_PX
  // Sub-quarter-pixel moves are invisible; skipping them keeps the frame loop
  // from writing twelve transforms per row on every single frame.
  if (bar.dataset.scale) {
    const painted = Number(bar.dataset.scale)
    if (Math.abs(painted - scale) < 0.002) return
  }
  bar.dataset.scale = scale.toFixed(4)
  bar.dataset.height = `${Math.round(clamped)}px`
  bar.style.transform = `scaleY(${scale.toFixed(4)})`
}

function paintCadenceBars(
  bars: Array<HTMLSpanElement | null>,
  heights: number[]
) {
  for (const index of BAR_INDEXES) {
    const bar = bars[index]
    if (bar) paintBarHeight(bar, heights[index])
  }
}

/**
 * Record the target height on the element while the frame loop catches up.
 * The visible height is the transform; the data attribute is what tests and
 * accessibility checks read, so it should describe where the bar is heading.
 */
function paintCadenceTargets(
  bars: Array<HTMLSpanElement | null>,
  heights: number[]
) {
  for (const index of BAR_INDEXES) {
    const bar = bars[index]
    if (!bar) continue
    const clamped = Math.max(2, Math.min(BAR_TRACK_PX, heights[index]))
    bar.dataset.height = `${Math.round(clamped)}px`
  }
}

/** Bar heights for a cadence window, left-padding a short window with idle. */
function cadenceTargets(values: number[]): number[] {
  const padded = values.slice(-BAR_COUNT)
  const leading = BAR_COUNT - padded.length
  return BAR_INDEXES.map((index) =>
    index >= leading ? cadenceBarHeight(padded[index - leading] ?? 0) : 2
  )
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
  const targetsRef = useRef<number[]>(
    Array.from({ length: BAR_COUNT }, () => 0)
  )
  const primedRef = useRef(false)
  const tokensRef = useRef(props.tokens)
  tokensRef.current = props.tokens
  const lastTokensRef = useRef<number | null>(null)
  const pendingRef = useRef(0)
  const spendPerMSRef = useRef(0)

  const streaming = props.active && props.state === 'streaming'
  const waiting = props.active && !streaming
  const cadence = props.cadence ?? null
  const hasCadence = streaming && cadence != null && cadence.length > 0
  const cadenceKey = hasCadence ? cadence.slice(-BAR_COUNT).join(',') : ''

  // Stable per-index ref callbacks: an inline arrow would be a fresh function on
  // every 150ms poll, and React would detach and re-attach all twelve refs on
  // each re-render of every live row.
  const barRefSetters = useMemo(
    () =>
      BAR_INDEXES.map((index) => (node: HTMLSpanElement | null) => {
        barsRef.current[index] = node
        node?.style.setProperty('--beat-i', String(index))
      }),
    []
  )

  // The backend publishes a time-aligned window of real 100ms buckets. Retarget
  // every bar, then let the shared frame loop ease toward it: the whole strip
  // moves at the frame rate instead of restarting a CSS transition four times a
  // second, which is what read as a stutter.
  useEffect(() => {
    if (!hasCadence || cadence == null) {
      primedRef.current = false
      return
    }
    const targets = cadenceTargets(cadence)
    targetsRef.current = targets
    if (!primedRef.current) {
      // First window (or the strip resumed after a pause): start on target,
      // otherwise every fresh meter would grow out of the baseline.
      primedRef.current = true
      levelsRef.current = [...targets]
      paintCadenceBars(barsRef.current, targets)
      return
    }
    paintCadenceTargets(barsRef.current, targets)

    const reduceMotion =
      window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
    if (reduceMotion) {
      levelsRef.current = [...targets]
      paintCadenceBars(barsRef.current, targets)
      return
    }

    // The loop stops once every bar settles, so a new window has to restart it;
    // keeping one alive per idle row would burn the frame budget for nothing.
    let lastFrameAt = 0
    let lastPaintAt = 0
    const unsubscribe = onFrame((now) => {
      const elapsed = lastFrameAt === 0 ? 16 : Math.min(now - lastFrameAt, 250)
      lastFrameAt = now
      const ease = 1 - Math.exp(-elapsed / CADENCE_EASE_MS)
      const levels = levelsRef.current
      const pending = targetsRef.current
      let unsettled = false
      for (const index of BAR_INDEXES) {
        const target = pending[index]
        const current = levels[index]
        let next = current + (target - current) * ease
        if (Math.abs(target - next) < SETTLE_EPSILON_PX) {
          next = target
        } else {
          unsettled = true
        }
        levels[index] = next
      }
      // Repaint at ~30fps: the bars glide at a rate the eye reads as continuous,
      // but twenty live rows stop writing twelve transforms on every single
      // frame, which is what starves the compositor on a long mobile list.
      if (!unsettled) {
        // Settling must land on the exact target, even if the last tick was
        // throttled, otherwise the strip would freeze a fraction short.
        paintCadenceBars(barsRef.current, levels)
        unsubscribe()
        return
      }
      if (now - lastPaintAt >= 30) {
        lastPaintAt = now
        paintCadenceBars(barsRef.current, levels)
      }
    })

    return unsubscribe
  }, [cadence, cadenceKey, hasCadence])

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
      const heights = BAR_INDEXES.map((index) =>
        Math.max(2, Math.round((levels[index] / peak) * BAR_TRACK_PX))
      )
      paintCadenceBars(barsRef.current, heights)
    }

    const timer = window.setInterval(tick, TICK_MS)
    return () => window.clearInterval(timer)
  }, [streaming, hasCadence])

  if (!props.active) return null

  return (
    <span
      data-slot='connection-beat-meter'
      data-scan={waiting ? '1' : '0'}
      data-cadence={hasCadence ? '1' : '0'}
      className='connection-beat flex h-3.5 items-end gap-[2px]'
      aria-hidden='true'
    >
      {BAR_INDEXES.map((index) => (
        <span
          key={index}
          ref={barRefSetters[index]}
          className={cn(
            'connection-beat-bar w-0.5 rounded-full',
            dotColorMap[props.tone]
          )}
        />
      ))}
    </span>
  )
}
