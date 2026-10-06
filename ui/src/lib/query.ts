import { QueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { APIError } from './api'
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      gcTime: 60_000,
      retry: (count, error) =>
        count < 2 && error instanceof APIError && (error.status === 0 || error.status >= 500),
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
      refetchOnWindowFocus: true,
    },
    mutations: { retry: false, gcTime: 0 },
  },
})
export function useVisibleInterval(milliseconds: number) {
  const [visible, setVisible] = useState(document.visibilityState !== 'hidden')
  useEffect(() => {
    const listener = () => setVisible(document.visibilityState !== 'hidden')
    document.addEventListener('visibilitychange', listener)
    return () => document.removeEventListener('visibilitychange', listener)
  }, [])
  return visible ? milliseconds : false
}
export function formatDate(value: string | undefined, language: string) {
  if (!value || value.startsWith('0001-')) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? '—'
    : new Intl.DateTimeFormat(language === 'fa' ? 'fa-IR' : 'en-GB', {
        dateStyle: 'medium',
        timeStyle: 'short',
      }).format(date)
}

// Each mounted form owns its own lifetime. Switching accounts/pages cancels
// queued HTTP work and ignores late responses; it cannot undo an accepted write.
export function useRequestSignal() {
  const controller = useRef(new AbortController())
  useEffect(() => {
    if (controller.current.signal.aborted) controller.current = new AbortController()
    const current = controller.current
    return () => current.abort()
  }, [])
  return useCallback(() => controller.current.signal, [])
}
