/**
 * One requestAnimationFrame pump shared by every live animation in the panel.
 *
 * Each stream used to own a rAF loop: the cadence strip in every row plus the
 * output-token counter, so a full mobile list queued dozens of callbacks for
 * the same frame. Subscribers are called from a single pump, and the pump
 * stops itself as soon as the last subscriber leaves, letting the page idle.
 */
type FrameListener = (now: number) => void

const listeners = new Set<FrameListener>()
let frameRequest = 0

function pump(now: number) {
  frameRequest = 0
  for (const listener of listeners) listener(now)
  if (listeners.size > 0) {
    frameRequest = window.requestAnimationFrame(pump)
  }
}

/** Subscribe to the shared frame clock. Returns an unsubscribe function. */
export function onFrame(listener: FrameListener): () => void {
  listeners.add(listener)
  if (frameRequest === 0) {
    frameRequest = window.requestAnimationFrame(pump)
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0 && frameRequest !== 0) {
      window.cancelAnimationFrame(frameRequest)
      frameRequest = 0
    }
  }
}
