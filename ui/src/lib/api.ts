import type { Scope, Session, WebhookFilter, WebhookPatch } from './types'
import { filterKey } from './webhook-filter'
export class APIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public retryAfter?: number,
  ) {
    super(message)
    this.name = 'APIError'
  }
}
let csrf = ''
let sessionGeneration = 0
export function setSession(session?: Session) {
  const next = session?.csrf_token ?? ''
  if (next !== csrf) sessionGeneration++
  csrf = next
}
export const apiEvents = new EventTarget()

// Queued aborts never get a fetch slot. Every request releases its slot, including
// errors, logout and account changes. No mutation is automatically retried.
export class RequestGate {
  private active = 0
  private waiting: Array<() => void> = []
  constructor(private limit = 3) {}
  async acquire(signal?: AbortSignal): Promise<() => void> {
    signal?.throwIfAborted()
    if (this.active >= this.limit)
      await new Promise<void>((resolve, reject) => {
        const start = () => {
          signal?.removeEventListener('abort', cancel)
          resolve()
        }
        const cancel = () => {
          this.waiting = this.waiting.filter((item) => item !== start)
          reject(signal?.reason ?? new DOMException('Aborted', 'AbortError'))
        }
        signal?.addEventListener('abort', cancel, { once: true })
        this.waiting.push(start)
      })
    // A released slot is reserved for this waiter (active is decremented only
    // when no waiter exists), avoiding a fourth fetch during a microtask race.
    else this.active++
    if (signal?.aborted) {
      this.release()
      signal.throwIfAborted()
    }
    let released = false
    return () => {
      if (!released) {
        released = true
        this.release()
      }
    }
  }
  private release() {
    const next = this.waiting.shift()
    if (next) next()
    else this.active--
  }
}
const gate = new RequestGate()
export type RequestOptions = {
  method?: string
  body?: unknown
  scope?: Scope
  signal?: AbortSignal
  auth?: boolean
}
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const generation = sessionGeneration
  const method = options.method ?? 'GET'
  const headers = new Headers({ Accept: 'application/json' })
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  if (method !== 'GET' && csrf) headers.set('X-CSRF-Token', csrf)
  if (options.scope) {
    headers.set('X-Device-Id', options.scope.id)
    headers.set('X-Device-Instance', options.scope.instance_id)
  }
  const release = await gate.acquire(options.signal)
  try {
    if (generation !== sessionGeneration) throw new DOMException('Session changed', 'AbortError')
    const response = await fetch(
      new URL(`${options.auth ? '' : 'api/'}${path}`, document.baseURI),
      {
        method,
        headers,
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        signal: options.signal,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
      },
    )
    const envelope = await response.json().catch(() => ({
      code: 'INVALID_RESPONSE',
      message: 'The server returned an invalid response.',
    }))
    if (generation !== sessionGeneration || options.signal?.aborted)
      throw new DOMException('Request is no longer current', 'AbortError')
    if (!response.ok) {
      if (
        response.status === 401 &&
        envelope.code === 'UI_UNAUTHORIZED' &&
        !(options.auth && method === 'POST')
      ) {
        setSession()
        apiEvents.dispatchEvent(new Event('unauthorized'))
      }
      if (response.status === 409 && envelope.code === 'DEVICE_INSTANCE_CHANGED')
        apiEvents.dispatchEvent(new CustomEvent('instance-changed', { detail: options.scope }))
      throw new APIError(
        response.status,
        envelope.code ?? 'REQUEST_FAILED',
        envelope.message ?? 'Request failed',
        Number(response.headers.get('Retry-After')) || undefined,
      )
    }
    return envelope.results as T
  } catch (error) {
    if (error instanceof APIError || (error instanceof DOMException && error.name === 'AbortError'))
      throw error
    throw new APIError(0, 'NETWORK_ERROR', 'The server could not be reached.')
  } finally {
    release()
  }
}
export function devicePath(scope: Scope, suffix = '') {
  return `devices/${encodeURIComponent(scope.id)}${suffix}`
}
export function webhookPatch(
  original: {
    webhook_url: string
    webhook_events: string[] | null
    webhook_filter?: WebhookFilter
  },
  url: string,
  events: string[],
  replaceSecret: boolean,
  secret: string,
  filter?: WebhookFilter,
) {
  const patch: WebhookPatch = {}
  if (url !== original.webhook_url) patch.webhook_url = url
  if (JSON.stringify(events) !== JSON.stringify(original.webhook_events ?? []))
    patch.webhook_events = events
  if (replaceSecret) patch.webhook_secret = secret
  if (filter !== undefined && filterKey(filter) !== filterKey(original.webhook_filter))
    patch.webhook_filter = filter
  return patch
}
