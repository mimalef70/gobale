import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import i18n from '../lib/i18n'
import { setSession } from '../lib/api'
import type { Device, ProviderID, Webhook } from '../lib/types'
import { WebhookSettings } from './webhook'

beforeEach(async () => {
  document.head.innerHTML = '<base href="http://localhost/gateway/ui/">'
  setSession({ csrf_token: 'synthetic-csrf', expires_at: '', absolute_expires_at: '' })
  await i18n.changeLanguage('en')
})
afterEach(() => vi.unstubAllGlobals())

function renderSettings(provider: ProviderID = 'bale') {
  let config: Webhook = {
    webhook_url: 'https://example.test/hook',
    webhook_events: ['message'],
    webhook_filter: { peers: [{ type: 'user', id: '123' }], directions: ['incoming'] },
    revision: 1,
    secret_configured: true,
  }
  const patches: unknown[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url, options: RequestInit) => {
      if (options.method === 'PATCH') {
        const patch = JSON.parse(String(options.body))
        patches.push(patch)
        config = { ...config, ...patch }
        expect(new Headers(options.headers).get('X-Device-Instance')).toBe('synthetic-instance')
        expect(new Headers(options.headers).get('X-CSRF-Token')).toBe('synthetic-csrf')
      }
      return new Response(JSON.stringify({ results: config }))
    }),
  )
  const device: Device = {
    provider,
    id: 'synthetic',
    instance_id: 'synthetic-instance',
    created_at: '',
    webhook: config,
    status: { auth: '', transport: '', recovery: '' },
    deliveries: { pending: 0, failed: 0, paused: 0 },
  }
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={cache}>
      <WebhookSettings device={device} />
    </QueryClientProvider>,
  )
  return patches
}

it('preserves saved filters and secrets when only the destination changes', async () => {
  const patches = renderSettings(),
    user = userEvent.setup()
  const url = await screen.findByLabelText('Destination URL')
  await user.clear(url)
  await user.type(url, 'https://example.test/new')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(patches).toEqual([{ webhook_url: 'https://example.test/new' }]))
})

it('validates, edits and clears saved delivery filters through the scoped webhook route', async () => {
  const patches = renderSettings(),
    user = userEvent.setup()
  await user.click(await screen.findByText('Delivery filters', { exact: true }))
  const peers = screen.getByLabelText('Include peers')
  await user.clear(peers)
  await user.type(peers, 'user:01')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('INVALID_WEBHOOK_FILTER')
  expect(patches).toHaveLength(0)
  await user.clear(peers)
  await user.type(peers, 'group:456')
  await user.type(screen.getByLabelText('Exclude peers'), 'user:789')
  await user.type(screen.getByLabelText('Include sender IDs'), '123')
  await user.type(screen.getByLabelText('Exclude sender IDs'), '789')
  await user.click(screen.getByLabelText('Groups'))
  await user.click(screen.getByLabelText('Unknown direction'))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() =>
    expect(patches).toEqual([
      {
        webhook_filter: {
          peers: [{ type: 'group', id: '456' }],
          exclude_peers: [{ type: 'user', id: '789' }],
          sender_ids: ['123'],
          exclude_sender_ids: ['789'],
          peer_types: ['group'],
          directions: ['incoming', 'unknown'],
        },
      },
    ]),
  )
  await waitFor(() => expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled())
  await user.click(screen.getByText('Delivery filters', { exact: true }))
  for (const label of [
    'Include peers',
    'Exclude peers',
    'Include sender IDs',
    'Exclude sender IDs',
  ])
    await user.clear(screen.getByLabelText(label))
  for (const label of ['Groups', 'Incoming', 'Unknown direction'])
    await user.click(screen.getByLabelText(label))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(patches[1]).toEqual({ webhook_filter: {} }))
})

it('submits Eitaa supergroup and classic-group filters without merging their IDs', async () => {
  const patches = renderSettings('eitaa')
  const user = userEvent.setup()
  await user.click(await screen.findByText('Delivery filters', { exact: true }))
  const peers = screen.getByLabelText('Include peers')
  await user.clear(peers)
  await user.type(peers, 'group:91\ngroup:channel_91')
  await user.type(screen.getByLabelText('Include sender IDs'), '91')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() =>
    expect(patches).toEqual([
      {
        webhook_filter: {
          peers: [
            { type: 'group', id: '91' },
            { type: 'group', id: 'channel_91' },
          ],
          sender_ids: ['91'],
          directions: ['incoming'],
        },
      },
    ]),
  )
})
