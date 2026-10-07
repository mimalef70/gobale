import { expect, test, type Page, type Route } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const scope = { id: 'support-demo', instance_id: 'synthetic-instance-A' }
const now = new Date().toISOString()
function device(id = scope.id, instance_id = scope.instance_id) {
  return {
    id,
    instance_id,
    account_id: '123456789012345678',
    created_at: now,
    webhook: { webhook_url: 'https://example.test/hook', webhook_events: ['message'], revision: 1 },
    status: { auth: 'authenticated', transport: 'connected', recovery: 'gap_detected' },
    deliveries: { pending: 3, failed: 1, paused: 1 },
  }
}
async function mock(page: Page, options: { count?: number; signedIn?: boolean } = {}) {
  let signedIn = options.signedIn ?? false
  let phase = 'auth_required'
  let challenge: object | null = null
  let webhook = {
    webhook_url: 'https://example.test/hook',
    webhook_events: ['message'],
    revision: 1,
    secret_configured: true,
    routing_mode: 'device',
    routing_rules: [
      {
        source: 'device',
        url: 'https://example.test/hook',
        events: ['message'],
        secret_configured: true,
      },
    ],
  }
  const recorded: {
    path: string
    method: string
    body: Record<string, unknown> | undefined
    headers: Record<string, string>
  }[] = []
  const devices = Array.from({ length: options.count ?? 1 }, (_, i) =>
    device(i === 0 ? scope.id : `demo-${i}`, i === 0 ? scope.instance_id : `instance-${i}`),
  )
  const deliveries = [
    {
      delivery_id: 'delivery-A',
      event_id: 'event-A',
      device_id: scope.id,
      url: 'https://example.test/hook',
      state: 'failed',
      attempts: 8,
      created_at: now,
      next_attempt_at: now,
      payload: { text: '<img src=x onerror="alert(1)">' },
    },
  ]
  let replaceOnMutation = false
  async function handler(route: Route) {
    const url = new URL(route.request().url())
    const marker = url.pathname.indexOf('/ui/')
    const path = url.pathname.slice(marker + 4)
    if (!path.startsWith('api/') && !path.startsWith('auth/')) {
      await route.continue()
      return
    }
    const req = route.request()
    const method = req.method()
    const body = req.postDataJSON() as Record<string, unknown> | undefined
    recorded.push({ path, method, body, headers: req.headers() })
    const answer = (results: unknown, status = 200, code = 'SUCCESS') =>
      route.fulfill({
        status,
        contentType: 'application/json',
        body: JSON.stringify({ code, message: code, results }),
      })
    if (path === 'auth/session') {
      if (method === 'POST') {
        signedIn = true
        return answer({ csrf_token: 'synthetic-csrf', expires_at: now, absolute_expires_at: now })
      }
      if (method === 'DELETE') {
        signedIn = false
        return answer(null)
      }
      return signedIn
        ? answer({ csrf_token: 'synthetic-csrf', expires_at: now, absolute_expires_at: now })
        : answer(null, 401, 'UI_UNAUTHORIZED')
    }
    if (!signedIn) return answer(null, 401, 'UI_UNAUTHORIZED')
    if (replaceOnMutation && method !== 'GET' && path.includes('/devices/'))
      return answer(null, 409, 'DEVICE_INSTANCE_CHANGED')
    if (path === 'api/devices/overview')
      return answer({ server_time: new Date().toISOString(), devices })
    if (path === 'api/devices' && method === 'POST') {
      expect(body?.device_id).toBeTruthy()
      expect(route.request().headers()['idempotency-key']).toBeTruthy()
      const d = device(String(body?.device_id), 'new-instance')
      devices.push(d)
      return answer(d, 201)
    }
    if (path.endsWith('/login') && method === 'GET')
      return answer({ state: phase, challenge, server_time: new Date().toISOString() })
    if (path.endsWith('/login') && method === 'POST') {
      phase = 'awaiting_code'
      challenge = {
        challenge_id: 'challenge-1',
        state: phase,
        expires_at: new Date(Date.now() + 120_000).toISOString(),
        resend_available_at: new Date(Date.now() + 30_000).toISOString(),
        masked_phone: '+1555****01',
      }
      return answer(challenge)
    }
    if (path.endsWith('/login/code')) {
      phase = 'awaiting_password'
      return answer(null, 401, 'PASSWORD_REQUIRED')
    }
    if (path.endsWith('/login/password')) {
      phase = 'authenticated'
      challenge = null
      return answer(null)
    }
    if (path.endsWith('/webhook')) {
      if (method === 'PATCH') webhook = { ...webhook, ...body }
      return answer(webhook)
    }
    if (path === 'api/deliveries') {
      const rows = url.searchParams.get('state')
        ? deliveries.filter((d) => d.state === url.searchParams.get('state'))
        : deliveries
      return answer(rows.map((d) => ({ ...d, payload: null })))
    }
    if (path === 'api/deliveries/delivery-A') return answer(deliveries[0])
    if (path.endsWith('/retry')) {
      deliveries[0].state = 'cancelled'
      return answer(null)
    }
    if (path.endsWith('/replay')) return answer([])
    if (path === 'api/app/info')
      return answer({
        name: 'GoBale',
        version: '1.0.0',
        release_stage: 'release',
        protocol_note: 'Synthetic test',
      })
    if (path === 'api/ready') return answer({ status: 'ready' })
    return answer(null)
  }
  await page.route('**/ui/**', handler)
  return {
    recorded,
    expire() {
      signedIn = false
    },
    replace() {
      replaceOnMutation = true
    },
    devices,
  }
}
async function login(page: Page) {
  await page.goto('./')
  await page.getByLabel('Username', { exact: true }).fill('admin')
  await page.getByLabel('Password', { exact: true }).fill('synthetic-password')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Accounts', exact: true })).toBeVisible()
}
test('sign in, create connection with real contract, then sign out without storing credentials', async ({
  page,
}) => {
  const api = await mock(page)
  await login(page)
  await page.getByRole('button', { name: 'Add account', exact: true }).click()
  await page.getByLabel('Connection name').fill('new-demo')
  await page.getByRole('button', { name: 'Create connection', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'new-demo', exact: true })).toBeVisible()
  expect(api.recorded.find((r) => r.path === 'api/devices' && r.method === 'POST')?.body).toEqual({
    device_id: 'new-demo',
  })
  const stored = await page.evaluate(() => ({
    local: Object.keys(localStorage),
    session: Object.keys(sessionStorage),
  }))
  expect(stored.local.sort()).toEqual(['gobale.language', 'gobale.theme'])
  expect(stored.session).toEqual([])
  await page.getByRole('button', { name: 'Sign out', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible()
})
test('resume challenge after refresh and preserve two-factor whitespace', async ({ page }) => {
  const api = await mock(page)
  await login(page)
  await page.getByRole('link', { name: 'Manage support-demo' }).click()
  await page.getByRole('link', { name: 'Connect account', exact: true }).click()
  await page.getByLabel('Phone number').fill('+15550000001')
  await page.getByRole('button', { name: 'Request login code' }).click()
  await expect(page.getByLabel('Login code')).toBeVisible()
  await page.reload()
  await expect(page.getByLabel('Login code')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Request a new code' })).toBeDisabled()
  await page.getByLabel('Login code').fill('11111')
  await page.getByRole('button', { name: 'Verify code' }).click()
  await expect(page.getByLabel('Two-step verification', { exact: true })).toBeVisible()
  await page.getByLabel('Two-step verification', { exact: true }).fill('  synthetic secret  ')
  await page.getByRole('button', { name: 'Verify password' }).click()
  await expect(page.getByRole('heading', { name: 'Account authenticated' })).toBeVisible()
  expect(api.recorded.find((r) => r.path.endsWith('/login/password'))?.body?.password).toBe(
    '  synthetic secret  ',
  )
})
test('webhook edit omits existing secret and renders effective routing', async ({ page }) => {
  const api = await mock(page)
  await login(page)
  await page.getByRole('link', { name: 'Manage support-demo' }).click()
  await page.getByRole('link', { name: 'Webhook', exact: true }).click()
  await expect(page.getByText('A signing secret is configured')).toBeVisible()
  await expect(page.getByText('https://example.test/hook', { exact: true })).toBeVisible()
  await page.getByLabel('Destination URL').fill('')
  await page.getByRole('button', { name: 'Save changes' }).click()
  const patch = api.recorded.find((r) => r.method === 'PATCH')
  expect(patch?.body).toEqual({ webhook_url: '' })
  expect(patch?.headers['x-device-instance']).toBe(scope.instance_id)
  expect(patch?.headers['x-csrf-token']).toBe('synthetic-csrf')
})
test('delivery filter controls remain accessible in English and Persian mobile views', async ({
  page,
}, testInfo) => {
  const api = await mock(page)
  await login(page)
  await page.getByRole('link', { name: 'Manage support-demo' }).click()
  await page.getByRole('link', { name: 'Webhook', exact: true }).click()
  await page.getByText('Delivery filters', { exact: true }).click()
  await page.getByLabel('Include peers', { exact: true }).fill('user:123\ngroup:456')
  await page.getByLabel('Exclude sender IDs', { exact: true }).fill('789')
  await page.getByLabel('Unknown direction', { exact: true }).check()
  let report = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(report.violations).toEqual([])
  await page.screenshot({
    path: testInfo.outputPath('webhook-filters-desktop.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'زبان فارسی' }).click()
  await page.getByRole('button', { name: 'تغییر تم' }).click()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByLabel('مخاطبان مجاز', { exact: true })).toHaveValue('user:123\ngroup:456')
  report = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(report.violations).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({
    path: testInfo.outputPath('webhook-filters-fa-mobile.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'ذخیرهٔ تغییرات' }).click()
  expect(api.recorded.find((r) => r.method === 'PATCH')?.body).toEqual({
    webhook_filter: {
      peers: [
        { type: 'user', id: '123' },
        { type: 'group', id: '456' },
      ],
      exclude_sender_ids: ['789'],
      directions: ['unknown'],
    },
  })
})
test('deliveries keep payload inert, distinguish retry/replay and scope all requests', async ({
  page,
}) => {
  const api = await mock(page)
  await login(page)
  await page.getByRole('link', { name: 'Deliveries', exact: true }).click()
  await page.getByLabel('Choose a connection', { exact: true }).selectOption(scope.instance_id)
  await page.getByRole('button', { name: 'Details', exact: true }).click()
  await expect(page.getByText(/<img src=x/)).toBeVisible()
  expect(await page.locator('img').count()).toBe(0)
  await page.getByRole('button', { name: 'Retry delivery', exact: true }).click()
  await expect(page.getByText(/retaining the event/)).toBeVisible()
  await page.getByRole('button', { name: 'Confirm', exact: true }).click()
  await expect(page.getByText('Request completed')).toBeVisible()
  expect(
    api.recorded
      .filter((r) => r.path.startsWith('api/deliveries'))
      .every((r) => r.headers['x-device-instance'] === scope.instance_id),
  ).toBe(true)
})
test('stale instance mutation is blocked and old selection cleared', async ({ page }) => {
  const api = await mock(page)
  await login(page)
  await page.getByRole('link', { name: 'Manage support-demo' }).click()
  await page.getByRole('button', { name: 'Log out of Bale' }).click()
  api.replace()
  await page.getByRole('button', { name: 'Confirm', exact: true }).click()
  await expect(
    page
      .locator('#main-content')
      .getByText('This connection was replaced. Select the current account before continuing.'),
  ).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Accounts', exact: true })).toBeVisible()
  expect(api.recorded.find((r) => r.path.endsWith('/logout'))?.headers['x-device-instance']).toBe(
    scope.instance_id,
  )
})
test('fifty accounts use one local snapshot without per-account polling', async ({ page }) => {
  const api = await mock(page, { count: 50 })
  await login(page)
  await expect(page.getByRole('row')).toHaveCount(51)
  expect(api.recorded.filter((r) => r.path === 'api/devices/overview')).toHaveLength(1)
  expect(
    api.recorded.filter((r) => /api\/devices\//.test(r.path) && r.path !== 'api/devices/overview'),
  ).toHaveLength(0)
})
test('English and Persian, dark and mobile views stay accessible', async ({ page }, testInfo) => {
  await mock(page)
  await login(page)
  await expect(page.locator('html')).toHaveAttribute('dir', 'ltr')
  let report = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(report.violations).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('accounts-desktop.png'), fullPage: true })
  await page.getByRole('button', { name: 'زبان فارسی' }).click()
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl')
  await page.getByRole('button', { name: 'تغییر تم' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('heading', { name: 'حساب‌ها', exact: true })).toBeVisible()
  report = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(report.violations).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({
    path: testInfo.outputPath('accounts-fa-mobile-dark.png'),
    fullPage: true,
  })
})
test('session expiry clears account content and returns to sign in', async ({ page }) => {
  const api = await mock(page)
  await login(page)
  api.expire()
  await page.getByRole('link', { name: 'Service', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible()
  await expect(page.getByText('support-demo', { exact: true })).toHaveCount(0)
})
