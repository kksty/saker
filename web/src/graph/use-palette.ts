import { useEffect, useState } from 'react'
import { DEFAULT_GRAPH_PALETTE, readGraphPalette } from './palette'

/** Canvas styles use resolved colors. Re-read the tokens only when the theme changes. */
export function useGraphPalette() {
  const [palette, setPalette] = useState(DEFAULT_GRAPH_PALETTE)
  useEffect(() => {
    if (typeof document === 'undefined' || !document.documentElement || typeof getComputedStyle !== 'function') return
    const root = document.documentElement
    const sync = () => setPalette(readGraphPalette(getComputedStyle(root)))
    sync()
    const observer = new MutationObserver(sync)
    observer.observe(root, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])
  return palette
}
