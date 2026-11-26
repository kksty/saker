import { useCallback, useEffect, useRef, useState } from 'react'

export interface CopyFeedback {
  /** The row the feedback belongs to (a playbook id, a brief path, …). */
  key: string
  status: 'ok' | 'failed'
}

/**
 * In-place clipboard feedback with one timer per copy. The previous timer is
 * cleared before a new copy starts: two copies in a row used to share one
 * timeout, and the first could erase the second's confirmation early. A failed
 * write is reported, not swallowed — the visible text stays selectable for a
 * manual copy.
 */
export function useCopyFeedback(okTimeoutMs = 1500) {
  const [feedback, setFeedback] = useState<CopyFeedback | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearTimer = useCallback(() => {
    if (timer.current !== null) {
      clearTimeout(timer.current)
      timer.current = null
    }
  }, [])

  useEffect(() => clearTimer, [clearTimer])

  const copy = useCallback(async (text: string, key: string): Promise<boolean> => {
    clearTimer()
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      // Do not start a timer: the failure message must stay until the operator
      // tries again or copies something else.
      setFeedback({ key, status: 'failed' })
      return false
    }
    setFeedback({ key, status: 'ok' })
    timer.current = setTimeout(() => {
      setFeedback((current) => (current?.key === key && current.status === 'ok' ? null : current))
      timer.current = null
    }, okTimeoutMs)
    return true
  }, [clearTimer, okTimeoutMs])

  return { feedback, copy, clear: clearTimer }
}
