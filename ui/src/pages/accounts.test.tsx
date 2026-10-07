import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import i18n from '../lib/i18n'
import { setSession } from '../lib/api'
import { Accounts } from './accounts'

beforeEach(async () => {
  document.head.innerHTML = '<base href="http://localhost/gateway/ui/">'
  setSession({ csrf_token: 'synthetic-csrf', expires_at: '', absolute_expires_at: '' })
  await i18n.changeLanguage('en')
})
afterEach(() => vi.unstubAllGlobals())

function renderAccounts() {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <MemoryRouter>
      <QueryClientProvider client={cache}>
        <Accounts overview={{ devices: [], server_time: '' }} />
      </QueryClientProvider>
    </MemoryRouter>,
  )
}

it('recovers a lost provisioning response with the same key and frozen request', async () => {
  const calls: Array<{ key: string | null; body: string }> = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url, options: RequestInit) => {
      const headers = new Headers(options.headers)
      calls.push({ key: headers.get('Idempotency-Key'), body: String(options.body) })
      expect(headers.get('X-CSRF-Token')).toBe('synthetic-csrf')
      if (calls.length === 1) throw new TypeError('synthetic lost response after commit')
      return new Response(
        JSON.stringify({ results: { id: 'first', instance_id: 'immutable-first' } }),
      )
    }),
  )
  renderAccounts()
  const user = userEvent.setup()
  await user.click(screen.getAllByRole('button', { name: 'Add account' })[0])
  await user.type(screen.getByLabelText('Connection name'), 'first')
  await user.click(screen.getByRole('button', { name: 'Create connection' }))
  expect(await screen.findByRole('alert')).toBeVisible()
  expect(calls).toHaveLength(1)
  await user.click(screen.getByRole('button', { name: 'Create connection' }))
  await waitFor(() => expect(calls).toHaveLength(2))
  expect(calls[0].key).toBeTruthy()
  expect(calls[1]).toEqual(calls[0])
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
})

it('uses a new provisioning key when the user changes the requested connection', async () => {
  const keys: Array<string | null> = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url, options: RequestInit) => {
      keys.push(new Headers(options.headers).get('Idempotency-Key'))
      throw new TypeError('synthetic network failure')
    }),
  )
  renderAccounts()
  const user = userEvent.setup()
  await user.click(screen.getAllByRole('button', { name: 'Add account' })[0])
  const name = screen.getByLabelText('Connection name')
  await user.type(name, 'first')
  await user.click(screen.getByRole('button', { name: 'Create connection' }))
  await screen.findByRole('alert')
  await user.clear(name)
  await user.type(name, 'second')
  await user.click(screen.getByRole('button', { name: 'Create connection' }))
  await waitFor(() => expect(keys).toHaveLength(2))
  expect(keys[0]).toBeTruthy()
  expect(keys[1]).toBeTruthy()
  expect(keys[1]).not.toBe(keys[0])
})
