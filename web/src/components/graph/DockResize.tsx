// React glue for the graph's resizable right-hand overlays. The arithmetic is in
// dock-width.ts; this file owns the pointer/keyboard gesture and the persisted
// width. One hook per page is enough: the pinned node panel, the 下发 dock and
// the hover card all use the same width, so opening one after another does not
// move the edge the operator just set.
//
// The bounds are measured against the graph stage (the surface the dock floats
// over), not `window.innerWidth`: the console window can be wide while the map
// is narrow once the rail is open, and a viewport-based bound then happily sizes
// a dock over the whole map. `useViewWidth()` publishes the canvas width, which
// is the stage width; before the first canvas paint (SSR/tests) the window is
// only a fallback so the hook always returns a usable width.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from 'react'
import {
  DOCK_DEFAULT_WIDTH,
  DOCK_KEY_STEP,
  DOCK_STORAGE_KEY,
  clampDockWidth,
  dockBounds,
  dockWidthFromDrag,
  type DockBounds,
} from './dock-width'

export interface DockSize extends DockBounds {
  width: number
  /** True while a drag is in progress (the handle shows its active line). */
  resizing: boolean
  onResizeStart: (event: ReactPointerEvent<HTMLElement>) => void
  onResizeKey: (event: ReactKeyboardEvent<HTMLElement>) => void
}

/** Width of the surface the dock floats over: the measured stage, else a fallback. */
function surfaceWidth(containerWidth: number | undefined): number {
  if (typeof containerWidth === 'number' && Number.isFinite(containerWidth) && containerWidth > 0) {
    return containerWidth
  }
  // SSR/test renders have no window; the bounds then fall back to the maximum.
  return typeof window === 'undefined' ? 0 : window.innerWidth || 0
}

function readStoredWidth(): number {
  try {
    const raw = window.localStorage.getItem(DOCK_STORAGE_KEY)
    const parsed = raw === null || raw.trim() === '' ? NaN : Number(raw)
    // Clamped by the caller, against the container it actually has.
    return Number.isFinite(parsed) ? parsed : DOCK_DEFAULT_WIDTH
  } catch {
    // Storage blocked (private mode): the width just does not persist.
    return DOCK_DEFAULT_WIDTH
  }
}

function writeStoredWidth(width: number) {
  try {
    window.localStorage.setItem(DOCK_STORAGE_KEY, String(width))
  } catch {
    /* private mode: the width just does not persist */
  }
}

export function useDockSize(containerWidth?: number): DockSize {
  const [stored, setStored] = useState(readStoredWidth)
  const [resizing, setResizing] = useState(false)
  // The stage is re-published by the canvas on every resize; keep the latest
  // width in a ref so a live pointer gesture always clamps against the current
  // surface instead of the one captured when the drag started.
  const surfaceRef = useRef(surfaceWidth(containerWidth))
  surfaceRef.current = surfaceWidth(containerWidth)
  // Clamp on read as well as on write: a window/map that shrank since the width
  // was stored must not report a width beyond its own bounds (the overlay's CSS
  // max-inline-size does the final guard against going off-screen).
  const width = clampDockWidth(stored, surfaceRef.current)
  const widthRef = useRef(width)
  widthRef.current = width
  // One cleanup for whichever gesture is live: the graph page unmounts (rail
  // navigation) while the pointer can still be down on the handle.
  const stopRef = useRef<(() => void) | null>(null)

  useEffect(() => () => stopRef.current?.(), [])

  const onResizeStart = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    // Left button / touch / pen only: a right-click must not start a resize.
    if (event.pointerType === 'mouse' && event.button !== 0) return
    event.preventDefault()
    const startX = event.clientX
    const startWidth = widthRef.current
    let latest = startWidth
    setResizing(true)
    const move = (e: PointerEvent) => {
      latest = dockWidthFromDrag(startWidth, startX, e.clientX, surfaceRef.current)
      setStored(latest)
    }
    const end = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', end)
      window.removeEventListener('pointercancel', end)
      window.removeEventListener('blur', end)
      stopRef.current = null
      setResizing(false)
      // Persist the last dragged value itself: the state update from the final
      // pointermove may not have rendered yet.
      writeStoredWidth(latest)
    }
    stopRef.current = end
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', end)
    window.addEventListener('pointercancel', end)
    // A button released outside the window never delivers pointerup here.
    window.addEventListener('blur', end)
  }, [])

  const onResizeKey = useCallback((event: ReactKeyboardEvent<HTMLElement>) => {
    // ← makes the dock wider (the handle is its left edge), → narrower.
    const step = event.key === 'ArrowLeft' ? DOCK_KEY_STEP : event.key === 'ArrowRight' ? -DOCK_KEY_STEP : 0
    if (step === 0) return
    event.preventDefault()
    const next = clampDockWidth(widthRef.current + step, surfaceRef.current)
    setStored(next)
    writeStoredWidth(next)
  }, [])

  return { width, resizing, ...dockBounds(surfaceRef.current), onResizeStart, onResizeKey }
}

/**
 * The dock's left-edge grab strip. It is a child of the overlay (which is
 * `overflow-hidden`), so it cannot be clipped by the rounded border; the 1px
 * line only appears on hover/focus to keep the map quiet. It is focusable so the
 * width is reachable without a pointer (← →), and `saker-dock-handle` widens it to
 * the 24px touch-target minimum on a coarse pointer.
 */
export function DockResizeHandle({ dock }: { dock: DockSize }) {
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="调整右侧面板宽度"
      aria-valuemin={dock.min}
      aria-valuemax={dock.max}
      aria-valuenow={dock.width}
      title="左右拖动调整宽度（← → 也可）"
      tabIndex={0}
      onPointerDown={dock.onResizeStart}
      onKeyDown={dock.onResizeKey}
      className="saker-dock-handle group absolute inset-y-0 left-0 z-20 cursor-col-resize touch-none select-none outline-none"
    >
      <span
        aria-hidden="true"
        className={`absolute inset-y-1 left-0 w-0.5 rounded-full transition-colors ${
          dock.resizing ? 'bg-primary/70' : 'bg-transparent group-hover:bg-hairline group-focus-visible:bg-ring/70'
        }`}
      />
    </div>
  )
}
