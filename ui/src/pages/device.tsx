import { useState } from 'react'
import { Link, NavLink, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, LogOut, RotateCcw, Trash2 } from 'lucide-react'
import type { Device, Overview } from '../lib/types'
import { devicePath, request } from '../lib/api'
import { formatDate, useRequestSignal } from '../lib/query'
import { Button, ErrorNotice, Modal, Notice, Status } from '../components/ui'
import { deviceURL } from './accounts'
import { AccountLogin } from './login'
import { WebhookSettings } from './webhook'
import { Deliveries } from './deliveries'
export function DevicePage({ overview }: { overview: Overview }) {
  const { t } = useTranslation()
  const { id, tab = 'overview' } = useParams()
  const [params] = useSearchParams()
  const device = overview.devices.find(
    (d) => d.id === id && d.instance_id === params.get('instance'),
  )
  if (!device)
    return (
      <>
        <Link className="back-link" to="/accounts">
          <ArrowLeft size={15} />
          {t('back')}
        </Link>
        <Notice>{t('changed')}</Notice>
      </>
    )
  return (
    <div key={device.instance_id}>
      <Link className="back-link" to="/accounts">
        <ArrowLeft size={15} />
        {t('back')}
      </Link>
      <div className="page-heading">
        <div>
          <p className="eyebrow">
            {t(`providerName.${device.provider}`)} · {t('connection')}
          </p>
          <h1 dir="auto">{device.id}</h1>
          <p>
            {t('accountID')}: <bdi className="mono">{device.account_id || t('notConnected')}</bdi>
          </p>
        </div>
        <Status value={device.status.transport} />
      </div>
      <nav className="tabs" aria-label={t('connection')}>
        {['overview', 'login', 'webhook', 'deliveries'].map((value) => (
          <NavLink key={value} to={deviceURL(device, value)}>
            {t(value)}
          </NavLink>
        ))}
      </nav>
      {tab === 'login' ? (
        <AccountLogin device={device} />
      ) : tab === 'webhook' ? (
        <WebhookSettings device={device} />
      ) : tab === 'deliveries' ? (
        <Deliveries devices={[device]} fixed={device} />
      ) : (
        <DeviceOverview device={device} />
      )}
    </div>
  )
}
function DeviceOverview({ device }: { device: Device }) {
  const getSignal = useRequestSignal()
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [action, setAction] = useState<'reconnect' | 'logout' | 'delete'>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  async function perform() {
    if (!action) return
    setBusy(true)
    setError(undefined)
    try {
      await request(devicePath(device, action === 'delete' ? '' : `/${action}`), {
        method: action === 'delete' ? 'DELETE' : 'POST',
        scope: device,
        signal: getSignal(),
      })
      await cache.cancelQueries({ predicate: (q) => q.queryKey.includes(device.instance_id) })
      cache.removeQueries({ predicate: (q) => q.queryKey.includes(device.instance_id) })
      await cache.invalidateQueries({ queryKey: ['overview'] })
      setAction(undefined)
      if (action === 'delete') navigate('/accounts')
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <section className="panel form-panel">
        <div className="section-heading">
          <div>
            <h2>{t('overview')}</h2>
            <p>{t('statusHint')}</p>
          </div>
        </div>
        <div className="connection-states">
          {(['authentication', 'transport', 'recovery'] as const).map((label, i) => (
            <div key={label}>
              <span>{t(label)}</span>
              <Status
                value={[device.status.auth, device.status.transport, device.status.recovery][i]}
              />
            </div>
          ))}
        </div>
        {['gap_detected', 'degraded'].includes(device.status.recovery) && (
          <Notice>{t('recoveryWarning')}</Notice>
        )}
        {device.status.unsupported_updates_observed && <Notice>{t('unsupportedUpdatesWarning')}</Notice>}
        {device.status.recovery_issue === 'legacy_unclassified' && <Notice>{t('legacyRecoveryWarning')}</Notice>}
        <dl className="details-list">
          <div>
            <dt>{t('created')}</dt>
            <dd>{formatDate(device.created_at, i18n.language)}</dd>
          </div>
          <div>
            <dt>{t('lastError')}</dt>
            <dd dir="auto">{device.status.last_error || t('noError')}</dd>
          </div>
        </dl>
      </section>
      <section className="panel lifecycle">
        <h2>{t('actions')}</h2>
        <div>
          <Button
            variant="secondary"
            onClick={() => {
              setError(undefined)
              setAction('reconnect')
            }}
          >
            <RotateCcw size={16} />
            {t('reconnect')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => {
              setError(undefined)
              setAction('logout')
            }}
          >
            <LogOut size={16} />
            {t('logoutAccount')}
          </Button>
          <Button
            variant="danger"
            onClick={() => {
              setError(undefined)
              setAction('delete')
            }}
          >
            <Trash2 size={16} />
            {t('deleteAccount')}
          </Button>
        </div>
      </section>
      <Modal
        open={!!action}
        onOpenChange={(open) => !open && !busy && setAction(undefined)}
        title={t(
          action === 'delete'
            ? 'deleteAccount'
            : action === 'logout'
              ? 'logoutAccount'
              : 'reconnect',
        )}
        description={t(
          action === 'delete' ? 'deleteHint' : action === 'logout' ? 'logoutHint' : 'reconnectHint',
        )}
      >
        <div className="target-account">
          <small>{t('target')}</small>
          <strong dir="auto">{device.id}</strong>
          <code dir="ltr">{device.account_id || t('notConnected')}</code>
        </div>
        <ErrorNotice error={error} />
        <div className="dialog-actions">
          <Button variant="secondary" disabled={busy} onClick={() => setAction(undefined)}>
            {t('cancel')}
          </Button>
          <Button
            busy={busy}
            variant={action === 'delete' ? 'danger' : 'primary'}
            onClick={() => void perform()}
          >
            {t('confirm')}
          </Button>
        </div>
      </Modal>
    </>
  )
}
