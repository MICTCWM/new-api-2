import { useCallback, useSyncExternalStore } from 'react'

/**
 * React hook for responsive media queries
 * @param query - CSS media query string (e.g., "(max-width: 640px)")
 * @returns boolean indicating if the query matches
 */
export function useMediaQuery(query: string): boolean {
  // Stable per query: a parent re-rendering must not re-subscribe the listener.
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      // Return early if window is not available (SSR)
      if (typeof window === 'undefined') {
        return () => {}
      }

      const media = window.matchMedia(query)
      media.addEventListener('change', onStoreChange)
      return () => media.removeEventListener('change', onStoreChange)
    },
    [query]
  )

  return useSyncExternalStore(
    subscribe,
    () => {
      // Client-side: return the current match state
      if (typeof window !== 'undefined') {
        return window.matchMedia(query).matches
      }
      return false
    },
    () => {
      // Server-side: return false as fallback
      return false
    }
  )
}
