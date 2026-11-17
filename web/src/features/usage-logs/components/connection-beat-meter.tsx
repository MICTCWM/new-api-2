import { useEffect, useRef } from 'react'

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
 * Attack/release easing, the way a hardware VU meter behaves: a burst snaps to
 * its new height within a couple of frames, while a pause falls back with a
 * short tail so the strip does not cut to idle. The slow single constant used
 * in rc.87 read as lag — smooth, but the burst arrived noticeably after it
 * happened upstream.
 */
const CADENCE_ATTACK_MS = 55
const CADENCE_RELEASE_MS = 190
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
  bar.dataset.height = `${Math.round(clamped)}px`
  bar.style.transform = `scaleY(${(clamped / BAR_TRACK_PX).toFixed(4)})`
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
    const unsubscribe = onFrame((now) => {
      const elapsed = lastFrameAt === 0 ? 16 : Math.min(now - lastFrameAt, 250)
      lastFrameAt = now
      const attack = 1 - Math.exp(-elapsed / CADENCE_ATTACK_MS)
      const release = 1 - Math.exp(-elapsed / CADENCE_RELEASE_MS)
      const levels = levelsRef.current
      const pending = targetsRef.current
      let unsettled = false
      for (const index of BAR_INDEXES) {
        const target = pending[index]
        const current = levels[index]
        const ease = target > current ? attack : release
        let next = current + (target - current) * ease
        if (Math.abs(target - next) < SETTLE_EPSILON_PX) {
          next = target
        } else {
          unsettled = true
        }
        levels[index] = next
      }
      paintCadenceBars(barsRef.current, levels)
      if (!unsettled) unsubscribe()
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
