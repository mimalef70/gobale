import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, apiEvents, RequestGate, request, setSession, webhookPatch } from './api'
beforeEach(() => {
  document.head.innerHTML = '<base href="http://localhost/gateway/ui/">'
  setSession()
})
describe('account-bound API client', () => {
  it('uses same-origin cookies, a scoped immutable instance and CSRF under a base path', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ results: { ok: true } })))
    vi.stubGlobal('fetch', fetcher)
    setSession({ csrf_token: 'synthetic-csrf', expires_at: '', absolute_expires_at: '' })
    await request('devices/demo/webhook', {
      method: 'PATCH',
      scope: { id: 'demo', instance_id: 'instance-A' },
      body: { webhook_secret: '  preserved  ' },
    })
    const [url, options] = fetcher.mock.calls[0]
    expect(url.href).toBe('http://localhost/gateway/ui/api/devices/demo/webhook')
    expect(options.credentials).toBe('same-origin')
    expect(options.redirect).toBe('error')
    expect(options.headers.get('X-Device-Instance')).toBe('instance-A')
    expect(options.headers.get('X-Device-Id')).toBe('demo')
    expect(options.headers.get('X-CSRF-Token')).toBe('synthetic-csrf')
    expect(JSON.parse(options.body).webhook_secret).toBe('  preserved  ')
  })
  it('never carries a selected account into a global request', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('{"results":null}'))
    vi.stubGlobal('fetch', fetcher)
    await request('devices/demo/logout', {
      method: 'POST',
      scope: { id: 'demo', instance_id: 'A' },
    })
    await request('devices/overview')
    expect(fetcher.mock.calls[1][1].headers.has('X-Device-Instance')).toBe(false)
  })
  it('reports expired sessions and replaced instances without retrying mutations', async () => {
    const listener = vi.fn()
    apiEvents.addEventListener('instance-changed', listener)
    const fetcher = vi
      .fn()
      .mockResolvedValue(new Response('{"code":"DEVICE_INSTANCE_CHANGED"}', { status: 409 }))
    vi.stubGlobal('fetch', fetcher)
    await expect(
      request('devices/demo/logout', { method: 'POST', scope: { id: 'demo', instance_id: 'old' } }),
    ).rejects.toMatchObject({ status: 409 })
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(listener).toHaveBeenCalledOnce()
    apiEvents.removeEventListener('instance-changed', listener)
    const expired = vi.fn()
    apiEvents.addEventListener('unauthorized', expired)
    fetcher.mockResolvedValue(new Response('{"code":"UI_UNAUTHORIZED"}', { status: 401 }))
    await expect(request('devices/overview')).rejects.toBeInstanceOf(APIError)
    expect(expired).toHaveBeenCalledOnce()
    apiEvents.removeEventListener('unauthorized', expired)
  })
})
describe('bounded request queue', () => {
  it('caps active requests, aborts queued requests and reserves handed-off slots', async () => {
    const gate = new RequestGate(3)
    const releases = await Promise.all([gate.acquire(), gate.acquire(), gate.acquire()])
    const abort = new AbortController()
    const canceled = gate.acquire(abort.signal)
    const checked = expect(canceled).rejects.toMatchObject({ name: 'AbortError' })
    abort.abort()
    await checked
    let admitted = false
    const fourth = gate.acquire().then((release) => {
      admitted = true
      return release
    })
    await Promise.resolve()
    expect(admitted).toBe(false)
    releases[0]()
    const fifth = gate.acquire()
    const releaseFourth = await fourth
    let fifthAdmitted = false
    void fifth.then(() => {
      fifthAdmitted = true
    })
    await Promise.resolve()
    expect(fifthAdmitted).toBe(false)
    releaseFourth()
    const releaseFifth = await fifth
    releaseFifth()
    releases[1]()
    releases[2]()
    const final = await gate.acquire()
    final()
  })
})
it('webhook PATCH omits unchanged fields and preserves exact replacement secrets', () => {
  const original = {
    webhook_url: 'https://example.test/hook',
    webhook_events: ['message', 'custom'],
  }
  expect(webhookPatch(original, original.webhook_url, ['message', 'custom'], false, '')).toEqual({})
  expect(webhookPatch(original, '', ['message', 'custom'], false, '')).toEqual({ webhook_url: '' })
  expect(
    webhookPatch(original, original.webhook_url, ['message', 'custom'], true, '  key  '),
  ).toEqual({ webhook_secret: '  key  ' })
})
it('provider AUTH_REQUIRED does not log the administrator out', async () => {
  const expired = vi.fn()
  apiEvents.addEventListener('unauthorized', expired)
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(new Response('{"code":"AUTH_REQUIRED"}', { status: 401 })),
  )
  await expect(
    request('devices/demo/reconnect', { method: 'POST', scope: { id: 'demo', instance_id: 'A' } }),
  ).rejects.toMatchObject({ code: 'AUTH_REQUIRED' })
  expect(expired).not.toHaveBeenCalled()
  apiEvents.removeEventListener('unauthorized', expired)
})
it('a late old-session response cannot log out a fresh session or change its selection', async () => {
  setSession({ csrf_token: 'old', expires_at: '', absolute_expires_at: '' })
  let finish!: (r: Response) => void
  const fetcher = vi.fn(
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve
      }),
  )
  vi.stubGlobal('fetch', fetcher)
  const expired = vi.fn()
  apiEvents.addEventListener('unauthorized', expired)
  const call = request('devices/overview')
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce())
  setSession({ csrf_token: 'new', expires_at: '', absolute_expires_at: '' })
  finish(new Response('{"code":"UI_UNAUTHORIZED"}', { status: 401 }))
  await expect(call).rejects.toMatchObject({ name: 'AbortError' })
  expect(expired).not.toHaveBeenCalled()
  apiEvents.removeEventListener('unauthorized', expired)
})
it('aborting a queued account mutation prevents provider contact', async () => {
  const pending: ((r: Response) => void)[] = []
  const fetcher = vi.fn(() => new Promise<Response>((resolve) => pending.push(resolve)))
  vi.stubGlobal('fetch', fetcher)
  const active = [request('devices/overview'), request('app/info'), request('ready')]
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3))
  const controller = new AbortController()
  const mutation = request('devices/demo/logout', {
    method: 'POST',
    scope: { id: 'demo', instance_id: 'A' },
    signal: controller.signal,
  })
  const cancelled = expect(mutation).rejects.toMatchObject({ name: 'AbortError' })
  controller.abort()
  await cancelled
  pending.forEach((resolve) => resolve(new Response('{"results":null}')))
  await Promise.all(active)
  expect(fetcher).toHaveBeenCalledTimes(3)
})
