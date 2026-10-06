import { useState } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { RotateCcw, Webhook } from 'lucide-react'
import type { Delivery, Device } from '../lib/types'
import { request } from '../lib/api'
import { formatDate, useRequestSignal, useVisibleInterval } from '../lib/query'
import { Button, Empty, ErrorNotice, Modal, Notice, Spinner, Status } from '../components/ui'
export function Deliveries({ devices, fixed }: { devices: Device[]; fixed?: Device }) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState('')
  const device = fixed ?? devices.find((d) => d.instance_id === selected)
  return (
    <>
      {!fixed && (
        <>
          <div className="page-heading">
            <div>
              <p className="eyebrow">{t('admin')}</p>
              <h1>{t('deliveries')}</h1>
              <p>{t('deliveriesHint')}</p>
            </div>
          </div>
          <div className="account-selector">
            <label htmlFor="delivery-account">{t('chooseAccount')}</label>
            <select
              id="delivery-account"
              value={device?.instance_id ?? ''}
              onChange={(e) => setSelected(e.target.value)}
            >
              <option value="">{t('chooseAccount')}</option>
              {devices.map((d) => (
                <option value={d.instance_id} key={d.instance_id}>
                  {d.id}
                  {d.account_id ? ` · ${d.account_id}` : ''}
                </option>
              ))}
            </select>
          </div>
        </>
      )}
      {device ? (
        <DeliveryList key={device.instance_id} device={device} />
      ) : (
        <section className="panel">
          <Empty icon={<Webhook size={28} />} title={t('chooseAccount')}>
            <p>{t('deliveriesHint')}</p>
          </Empty>
        </section>
      )}
    </>
  )
}
function DeliveryList({ device }: { device: Device }) {
  const { t, i18n } = useTranslation()
  const [state, setState] = useState('')
  const [detail, setDetail] = useState<string>()
  const interval = useVisibleInterval(15_000)
  const list = useInfiniteQuery({
    queryKey: ['deliveries', device.instance_id, state],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      request<Delivery[]>(
        `deliveries?include_payload=false&limit=25&offset=${pageParam}${state ? `&state=${encodeURIComponent(state)}` : ''}`,
        { scope: device, signal },
      ),
    getNextPageParam: (last, _all, offset) => (last.length === 25 ? offset + 25 : undefined),
    refetchInterval: interval,
  })
  // Offset pagination can overlap while new rows arrive. Stable delivery IDs
  // prevent a duplicated row; there is deliberately no fabricated total count.
  const rows = [...new Map(list.data?.pages.flat().map((d) => [d.delivery_id, d]) ?? []).values()]
  return (
    <>
      <section className="panel">
        <div className="panel-toolbar">
          <h2>{t('deliveries')}</h2>
          <div className="filters">
            <select
              value={state}
              onChange={(e) => setState(e.target.value)}
              aria-label={t('allStates')}
            >
              <option value="">{t('allStates')}</option>
              {['queued', 'retry', 'delivering', 'failed', 'paused', 'delivered', 'cancelled'].map(
                (value) => (
                  <option value={value} key={value}>
                    {t(`status.${value}`)}
                  </option>
                ),
              )}
            </select>
            <button
              className="icon-button"
              title={t('refresh')}
              aria-label={t('refresh')}
              onClick={() => void list.refetch()}
              disabled={list.isFetching}
            >
              <RotateCcw size={17} />
            </button>
          </div>
        </div>
        <ErrorNotice error={list.error} />
        {list.isPending ? (
          <Spinner />
        ) : !rows.length ? (
          <Empty icon={<Webhook size={28} />} title={t('noDeliveries')}>
            <p>{t('noDeliveriesHint')}</p>
          </Empty>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t('event')}</th>
                  <th>{t('state')}</th>
                  <th>{t('destinationShort')}</th>
                  <th>{t('attempts')}</th>
                  <th>{t('timestamp')}</th>
                  <th>
                    <span className="sr-only">{t('details')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.delivery_id}>
                    <td>
                      <code className="truncate" dir="ltr" title={row.event_id}>
                        {row.event_id}
                      </code>
                    </td>
                    <td>
                      <Status value={row.state} />
                    </td>
                    <td>
                      <span className="truncate url" dir="ltr" title={row.url}>
                        {row.url}
                      </span>
                    </td>
                    <td>{row.attempts}</td>
                    <td className="nowrap">{formatDate(row.created_at, i18n.language)}</td>
                    <td>
                      <button className="table-action" onClick={() => setDetail(row.delivery_id)}>
                        {t('details')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {list.hasNextPage && (
          <div className="load-more">
            <Button
              variant="secondary"
              busy={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {t('loadMore')}
            </Button>
          </div>
        )}
      </section>
      {detail && (
        <DeliveryDetail
          key={detail}
          device={device}
          id={detail}
          close={() => setDetail(undefined)}
        />
      )}
    </>
  )
}
function DeliveryDetail({ device, id, close }: { device: Device; id: string; close(): void }) {
  const getSignal = useRequestSignal()
  const { t, i18n } = useTranslation()
  const cache = useQueryClient()
  const data = useQuery({
    queryKey: ['delivery', device.instance_id, id],
    queryFn: ({ signal }) =>
      request<Delivery>(`deliveries/${encodeURIComponent(id)}`, { scope: device, signal }),
  })
  const [confirm, setConfirm] = useState<'retry' | 'replay'>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const [done, setDone] = useState(false)
  async function perform() {
    if (!confirm) return
    setBusy(true)
    setError(undefined)
    try {
      await request(`deliveries/${encodeURIComponent(id)}/${confirm}`, {
        method: 'POST',
        scope: device,
        signal: getSignal(),
      })
      setConfirm(undefined)
      setDone(true)
      await Promise.all([
        cache.invalidateQueries({ queryKey: ['deliveries', device.instance_id] }),
        cache.invalidateQueries({ queryKey: ['delivery', device.instance_id, id] }),
        cache.invalidateQueries({ queryKey: ['overview'] }),
      ])
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  const delivery = data.data
  return (
    <Modal
      open
      onOpenChange={(open) => !open && !busy && close()}
      title={t('deliveryDetail')}
      description={`${device.id} · ${device.account_id || t('notConnected')}`}
    >
      <ErrorNotice error={data.error} />
      {data.isPending && <Spinner />}
      {delivery && (
        <>
          <dl className="details-list">
            <div>
              <dt>{t('deliveryID')}</dt>
              <dd>
                <code dir="ltr">{delivery.delivery_id}</code>
              </dd>
            </div>
            <div>
              <dt>{t('eventID')}</dt>
              <dd>
                <code dir="ltr">{delivery.event_id}</code>
              </dd>
            </div>
            <div>
              <dt>{t('state')}</dt>
              <dd>
                <Status value={delivery.state} />
              </dd>
            </div>
            <div>
              <dt>{t('destination')}</dt>
              <dd className="url" dir="ltr">
                {delivery.url}
              </dd>
            </div>
            <div>
              <dt>{t('attempts')}</dt>
              <dd>{delivery.attempts}</dd>
            </div>
            <div>
              <dt>{t('nextAttempt')}</dt>
              <dd>{formatDate(delivery.next_attempt_at, i18n.language)}</dd>
            </div>
            {delivery.last_error && (
              <div>
                <dt>{t('lastError')}</dt>
                <dd dir="auto">{delivery.last_error}</dd>
              </div>
            )}
          </dl>
          <h3 className="payload-heading">{t('payload')}</h3>
          <pre className="payload" dir="ltr">
            {JSON.stringify(delivery.payload ?? null, null, 2)}
          </pre>
          <ErrorNotice error={error} />
          {done && <Notice success>{t('actionDone')}</Notice>}
          {confirm ? (
            <div className="confirmation">
              <p>{t(confirm === 'retry' ? 'retryHint' : 'replayHint')}</p>
              <div className="dialog-actions">
                <Button variant="secondary" onClick={() => setConfirm(undefined)} disabled={busy}>
                  {t('cancel')}
                </Button>
                <Button onClick={() => void perform()} busy={busy}>
                  {t('confirm')}
                </Button>
              </div>
            </div>
          ) : (
            <div className="dialog-actions">
              <Button
                variant="secondary"
                onClick={() => {
                  setConfirm('replay')
                  setDone(false)
                }}
              >
                {t('replay')}
              </Button>
              {['failed', 'retry'].includes(delivery.state) && (
                <Button
                  onClick={() => {
                    setConfirm('retry')
                    setDone(false)
                  }}
                >
                  {t('retry')}
                </Button>
              )}
            </div>
          )}
        </>
      )}
    </Modal>
  )
}
