import { useRef, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowUpRight,
  CircleCheck,
  Plus,
  Search,
  Smartphone,
  Webhook,
  TriangleAlert,
} from 'lucide-react'
import type { Device, Overview, Provider, ProviderID } from '../lib/types'
import { useRequestSignal } from '../lib/query'
import { request } from '../lib/api'
import { Button, Empty, ErrorNotice, Field, Modal, Status } from '../components/ui'
export function deviceURL(device: { id: string; instance_id: string }, tab = 'overview') {
  return `/accounts/${encodeURIComponent(device.id)}/${tab}?instance=${encodeURIComponent(device.instance_id)}`
}
export function Accounts({ overview }: { overview: Overview }) {
  const { t } = useTranslation()
  const getSignal = useRequestSignal()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState('')
  const [providerFilter, setProviderFilter] = useState('')
  const [provider, setProvider] = useState<ProviderID | ''>('')
  const providers = useQuery({
    queryKey: ['providers'],
    queryFn: ({ signal }) => request<Provider[]>('app/providers', { signal }),
  })
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const provision = useRef<{ name: string; provider: ProviderID; key: string } | null>(null)
  const devices = overview.devices ?? []
  const attention = (d: Device) =>
    ['gap_detected', 'degraded'].includes(d.status.recovery) || d.deliveries.failed > 0
  const visible = devices.filter(
    (d) =>
      `${d.id} ${d.account_id ?? ''} ${d.provider}`.toLowerCase().includes(search.toLowerCase()) &&
      (!providerFilter || d.provider === providerFilter) &&
      (!filter ||
        (filter === 'connected'
          ? d.status.transport === 'connected'
          : filter === 'attention'
            ? attention(d)
            : d.status.transport !== 'connected')),
  )
  async function create(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const alias = name.trim()
      if (!provider || !providers.data?.some((item) => item.id === provider && item.enabled)) return
      if (
        !provision.current ||
        provision.current.name !== alias ||
        provision.current.provider !== provider
      )
        provision.current = { name: alias, provider, key: crypto.randomUUID() }
      const device = await request<Device>('devices', {
        method: 'POST',
        body: { device_id: alias, provider },
        idempotencyKey: provision.current.key,
        signal: getSignal(),
      })
      await cache.invalidateQueries({ queryKey: ['overview'] })
      setCreateOpen(false)
      setName('')
      setProvider('')
      provision.current = null
      navigate(deviceURL(device, 'login'))
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">{t('admin')}</p>
          <h1>{t('accounts')}</h1>
          <p>{t('accountsHint')}</p>
        </div>
        <Button
          onClick={() => {
            setError(undefined)
            setCreateOpen(true)
          }}
        >
          <Plus size={17} />
          {t('addAccount')}
        </Button>
      </div>
      <div className="stats-grid">
        {[
          { label: 'total', value: devices.length, icon: Smartphone },
          {
            label: 'healthy',
            value: devices.filter((d) => d.status.transport === 'connected').length,
            icon: CircleCheck,
          },
          {
            label: 'queued',
            value: devices.reduce((a, d) => a + d.deliveries.pending, 0),
            icon: Webhook,
          },
          {
            label: 'failed',
            value: devices.reduce((a, d) => a + d.deliveries.failed, 0),
            icon: TriangleAlert,
          },
        ].map((stat) => (
          <div className="stat" key={stat.label}>
            <div>
              <span>{t(stat.label)}</span>
              <strong>{stat.value.toLocaleString()}</strong>
            </div>
            <stat.icon size={23} aria-hidden />
          </div>
        ))}
      </div>
      <section className="panel">
        <div className="panel-toolbar">
          <h2>
            {t('allAccounts')} <span className="count">{devices.length}</span>
          </h2>
          <div className="filters">
            <select
              aria-label={t('allProviders')}
              value={providerFilter}
              onChange={(e) => setProviderFilter(e.target.value)}
            >
              <option value="">{t('allProviders')}</option>
              {['bale', 'eitaa', 'rubika'].map((id) => (
                <option key={id} value={id}>
                  {t(`providerName.${id}`)}
                </option>
              ))}
            </select>
            <div className="search">
              <Search size={16} aria-hidden />
              <input
                aria-label={t('search')}
                placeholder={t('search')}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <select
              aria-label={t('allStatuses')}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            >
              <option value="">{t('allStatuses')}</option>
              <option value="connected">{t('connected')}</option>
              <option value="attention">{t('attention')}</option>
              <option value="offline">{t('offline')}</option>
            </select>
          </div>
        </div>
        {!visible.length ? (
          <Empty
            icon={<Smartphone size={28} />}
            title={t(devices.length ? 'noResults' : 'noAccounts')}
          >
            <p>{t(devices.length ? 'adjustSearch' : 'noAccountsHint')}</p>
            {!devices.length && (
              <Button variant="secondary" onClick={() => setCreateOpen(true)}>
                <Plus size={16} />
                {t('addAccount')}
              </Button>
            )}
          </Empty>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t('connection')}</th>
                  <th>{t('authentication')}</th>
                  <th>{t('transport')}</th>
                  <th>{t('recovery')}</th>
                  <th>{t('deliveries')}</th>
                  <th>
                    <span className="sr-only">{t('actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {visible.map((d) => (
                  <tr key={d.instance_id}>
                    <td>
                      <div className="account-cell">
                        <div className="account-avatar" aria-hidden>
                          {d.id.slice(0, 2).toUpperCase()}
                        </div>
                        <div>
                          <Link className="account-name" dir="auto" to={deviceURL(d)}>
                            {d.id}
                          </Link>
                          <small>{t(`providerName.${d.provider}`)}</small>
                          <small dir="ltr">{d.account_id || t('notConnected')}</small>
                        </div>
                      </div>
                    </td>
                    <td>
                      <Status value={d.status.auth} />
                    </td>
                    <td>
                      <Status value={d.status.transport} />
                    </td>
                    <td>
                      <Status value={d.status.recovery} />
                    </td>
                    <td>
                      <span className={d.deliveries.failed ? 'error-text' : 'muted'}>
                        {d.deliveries.pending} / {d.deliveries.failed}
                      </span>
                      <small className="cell-hint">
                        {t('status.pending')} / {t('status.failed')}
                      </small>
                    </td>
                    <td>
                      <Link
                        className="table-action"
                        to={deviceURL(d)}
                        aria-label={`${t('manage')} ${d.id}`}
                      >
                        {t('manage')}
                        <ArrowUpRight size={15} />
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <Modal
        open={createOpen}
        onOpenChange={(open) => !busy && setCreateOpen(open)}
        title={t('createTitle')}
        description={t('createHint')}
      >
        <form onSubmit={create}>
          <Field id="connection-provider" label={t('provider')} hint={t('providerImmutable')}>
            <select
              id="connection-provider"
              value={provider}
              required
              disabled={busy || providers.isPending}
              aria-describedby="connection-provider-hint"
              onChange={(e) => setProvider(e.target.value as ProviderID | '')}
            >
              <option value="">{t('chooseProvider')}</option>
              {providers.data?.map((item) => (
                <option key={item.id} value={item.id} disabled={!item.enabled}>
                  {t(`providerName.${item.id}`)}
                  {!item.enabled ? ` — ${t('providerUnavailable')}` : ''}
                </option>
              ))}
            </select>
          </Field>
          <ErrorNotice error={providers.error} />
          <Field id="connection-name" label={t('connectionID')} hint={t('nameHint')}>
            <input
              id="connection-name"
              dir="ltr"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              autoComplete="off"
              maxLength={64}
              pattern="[A-Za-z0-9][A-Za-z0-9_.-]{0,63}"
              autoFocus
              aria-describedby="connection-name-hint"
            />
          </Field>
          <ErrorNotice error={error} />
          <div className="dialog-actions">
            <Button
              type="button"
              variant="secondary"
              onClick={() => setCreateOpen(false)}
              disabled={busy}
            >
              {t('cancel')}
            </Button>
            <Button
              busy={busy}
              type="submit"
              disabled={
                !provider || !providers.data?.some((item) => item.id === provider && item.enabled)
              }
            >
              {t('create')}
            </Button>
          </div>
        </form>
      </Modal>
    </>
  )
}
