import { useEffect, useRef, useState } from 'react'

import { formatNumber } from '@/lib/format'

import { CONNECTION_EMPTY_VALUE } from '../lib/connection-log-display'
import { onFrame } from '../lib/frame-loop'
import type { ConnectionLogItem } from '../types'

/**
 * Catch-up time constant. Short enough that the number snaps to a new total
 * almost immediately, long enough that a poll landing mid-digit still reads as
 * a count instead of a teleport.
 */
const OUTPUT_COUNT_CATCHUP_MS = 90
/** Repaint the number at 20fps; a per-frame React update on every live row was
 * competing with the cadence strip on long mobile lists. */
const OUTPUT_COUNT_PAINT_MS = 50
/** Guards the easing against long frame gaps (background tab, heavy render). */
const OUTPUT_COUNT_MAX_FRAME_MS = 250

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
    // instead of jumping ahead and freezing until the next poll. The chase runs
    // on the panel's shared frame clock, not a rAF loop per row.
    let lastFrameAt = performance.now()
    let lastPaintAt = 0
    let unsubscribe: (() => void) | null = null
    const step = (now: number) => {
      const elapsed = Math.min(now - lastFrameAt, OUTPUT_COUNT_MAX_FRAME_MS)
      lastFrameAt = now
      const target = targetRef.current
      const current = displayValueRef.current
      // Caught up: paint the exact final integer and stop scheduling frames
      // until the value changes again (the effect restarts the chase).
      if (current === target) {
        setDisplayValue(Math.round(target))
        unsubscribe?.()
        return
      }
      const gap = target - current
      let delta = gap * (1 - Math.exp(-elapsed / OUTPUT_COUNT_CATCHUP_MS))
      if (Math.abs(delta) < 1) delta = gap > 0 ? 1 : -1
      const next =
        gap > 0
          ? Math.min(current + delta, target)
          : Math.max(current + delta, target)
      displayValueRef.current = next
      if (now - lastPaintAt >= OUTPUT_COUNT_PAINT_MS) {
        lastPaintAt = now
        setDisplayValue(Math.round(next))
      }
    }

    unsubscribe = onFrame(step)
    return () => unsubscribe?.()
  }, [props.active, props.value])

  return <>{formatNumber(displayValue)}</>
}

/** Output tokens read `--` until the first token, then animate up to the total. */
export function ConnectionOutputTokens(props: { item: ConnectionLogItem }) {
  if (props.item.output_tokens == null) {
    return (
      <span className='text-muted-foreground'>{CONNECTION_EMPTY_VALUE}</span>
    )
  }

  return (
    <AnimatedOutputTokens
      value={props.item.output_tokens}
      active={props.item.active && props.item.stream}
    />
  )
}
