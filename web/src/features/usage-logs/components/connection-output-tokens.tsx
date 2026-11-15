import { useEffect, useRef, useState } from 'react'

import { formatNumber } from '@/lib/format'

import { CONNECTION_EMPTY_VALUE } from '../lib/connection-log-display'
import type { ConnectionLogItem } from '../types'

/** Time constant of the output-token catch-up: smaller = snappier counting. */
const OUTPUT_COUNT_CATCHUP_MS = 220
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
    // instead of jumping ahead and freezing until the next poll.
    let animationFrame = 0
    let lastFrameAt = performance.now()
    const step = (now: number) => {
      const elapsed = Math.min(now - lastFrameAt, OUTPUT_COUNT_MAX_FRAME_MS)
      lastFrameAt = now
      const target = targetRef.current
      const current = displayValueRef.current
      // Caught up: stop scheduling frames until the value changes again (the
      // effect below restarts the chase on the next poll).
      if (current === target) return
      const gap = target - current
      let delta = gap * (1 - Math.exp(-elapsed / OUTPUT_COUNT_CATCHUP_MS))
      if (Math.abs(delta) < 1) delta = gap > 0 ? 1 : -1
      const next =
        gap > 0
          ? Math.min(current + delta, target)
          : Math.max(current + delta, target)
      displayValueRef.current = next
      setDisplayValue(Math.round(next))
      animationFrame = requestAnimationFrame(step)
    }

    animationFrame = requestAnimationFrame(step)
    return () => cancelAnimationFrame(animationFrame)
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
