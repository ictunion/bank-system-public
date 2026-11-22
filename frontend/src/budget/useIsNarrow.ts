import { useSyncExternalStore } from 'react'

// Phone-width breakpoint; keep in sync with the max-width media query in index.css.
const narrowQuery = window.matchMedia('(max-width: 600px)')

function subscribe(onChange: () => void) {
  narrowQuery.addEventListener('change', onChange)
  return () => narrowQuery.removeEventListener('change', onChange)
}

export function useIsNarrow(): boolean {
  return useSyncExternalStore(subscribe, () => narrowQuery.matches)
}
