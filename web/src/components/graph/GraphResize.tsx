import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent as ReactPointerEvent, type RefObject } from 'react'

export interface OverlaySize {
  w: number
  h: number
}

const corners = {
  'bottom-right': { x: 1, y: 1, className: 'bottom-0 right-0 cursor-nwse-resize rounded-br-xl' },
  'top-left': { x: -1, y: -1, className: 'left-0 top-0 cursor-nwse-resize rounded-tl-xl' },
}
type Corner = keyof typeof corners

export function readOverlaySize(key: string): OverlaySize | null {
  try {
    const size = JSON.parse(window.localStorage.getItem(key) ?? 'null') as OverlaySize | null
    return size && Number.isFinite(size.w) && Number.isFinite(size.h) && size.w > 0 && size.h > 0 ? size : null
  } catch {
    return null
  }
}

function saveOverlaySize(key: string, size: OverlaySize | null) {
  try {
    if (size) window.localStorage.setItem(key, JSON.stringify(size))
    else window.localStorage.removeItem(key)
  } catch {
    // Resizing stays available when browser storage is disabled.
  }
}

function sameSize(a: OverlaySize, b: OverlaySize) {
  return a.w === b.w && a.h === b.h
}

export function useGraphResize(
  key: string,
  corner: Corner,
  measure: (saved: OverlaySize | null) => OverlaySize | null,
  clamp: (size: OverlaySize) => OverlaySize,
) {
  const [size, setSize] = useState(() => readOverlaySize(key))
  const finishDrag = useRef<(() => void) | null>(null)
  const clampSize = useRef(clamp)
  clampSize.current = clamp
  const direction = corners[corner]

  useEffect(() => () => finishDrag.current?.(), [key])

  function nextSize(start: OverlaySize, saved: OverlaySize | null, dx: number, dy: number) {
    const next = clampSize.current({ w: start.w + dx * direction.x, h: start.h + dy * direction.y })
    return {
      w: dx && next.w !== start.w ? next.w : (saved?.w ?? start.w),
      h: dy && next.h !== start.h ? next.h : (saved?.h ?? start.h),
    }
  }

  const handle = {
    onPointerDown(event: ReactPointerEvent<HTMLElement>) {
      if (finishDrag.current || (event.pointerType === 'mouse' && event.button !== 0)) return
      const measured = measure(size)
      if (!measured) return
      const start = measured
      event.preventDefault()
      event.stopPropagation()
      const { pointerId, clientX, clientY } = event
      const base = size ?? start
      let latest: OverlaySize | null = null
      const listeners = new AbortController()
      const options = { capture: true, signal: listeners.signal }

      function move(next: PointerEvent) {
        if (next.pointerId !== pointerId) return
        next.preventDefault()
        next.stopPropagation()
        const value = nextSize(start, size, next.clientX - clientX, next.clientY - clientY)
        latest = sameSize(value, base) ? null : value
        setSize(latest ?? size)
      }

      function finish() {
        listeners.abort()
        finishDrag.current = null
        if (latest) saveOverlaySize(key, latest)
      }

      // The grip moves with the card. Follow the pointer even when it leaves
      // that element or the card scrolls underneath it.
      finishDrag.current = finish
      window.addEventListener('pointermove', move, options)
      window.addEventListener('pointerup', (next) => {
        if (next.pointerId !== pointerId) return
        move(next)
        finish()
      }, options)
      window.addEventListener('pointercancel', (next) => {
        if (next.pointerId === pointerId) finish()
      }, options)
      window.addEventListener('blur', finish, options)
    },
    onKeyDown(event: KeyboardEvent<HTMLElement>) {
      if (finishDrag.current) return
      if (event.key === 'Home') {
        event.preventDefault()
        setSize(null)
        saveOverlaySize(key, null)
        return
      }
      const step = event.shiftKey ? 32 : 8
      const dx = event.key === 'ArrowRight' ? step : event.key === 'ArrowLeft' ? -step : 0
      const dy = event.key === 'ArrowDown' ? step : event.key === 'ArrowUp' ? -step : 0
      if (!dx && !dy) return
      event.preventDefault()
      const start = measure(size)
      if (!start) return
      const next = nextSize(start, size, dx, dy)
      if (sameSize(next, size ?? start)) return
      setSize(next)
      saveOverlaySize(key, next)
    },
  }
  return { size, corner, handle }
}

export function useGraphCardResize(key: string, corner: Corner, card: RefObject<HTMLDivElement>) {
  return useGraphResize(key, corner, () => {
    const el = card.current
    return el ? { w: el.offsetWidth, h: el.offsetHeight } : null
  }, (size) => {
    const el = card.current
    if (!el) return size
    const css = getComputedStyle(el)
    const fit = (value: number, min: string, max: string) =>
      Math.round(Math.max(parseFloat(min) || 0, Math.min(parseFloat(max) || Infinity, value)))
    return { w: fit(size.w, css.minWidth, css.maxWidth), h: fit(size.h, css.minHeight, css.maxHeight) }
  })
}

export function GraphResizeHandle({ resize, label, testId }: {
  resize: ReturnType<typeof useGraphResize>
  label: string
  testId: string
}) {
  return (
    <span
      {...resize.handle}
      role="separator"
      aria-label={`${label}：拖动调整大小，方向键微调，Home 恢复默认`}
      title="拖动调整大小 · 方向键微调 · Home 恢复默认"
      data-testid={testId}
      className={`saker-graph-grip pointer-events-auto absolute z-10 grid h-6 w-6 touch-none select-none place-items-center text-ink-3 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring ${corners[resize.corner].className}`}
      tabIndex={0}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true" className={`size-4 ${resize.corner === 'top-left' ? 'rotate-180' : ''}`}>
        <path d="M5 13 13 5m-4.5 8.5 5-5M12 14l2-2" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
      </svg>
    </span>
  )
}
