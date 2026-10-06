import { useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { LockKeyhole, Webhook as WebhookIcon } from 'lucide-react'
import type { Device, Webhook } from '../lib/types'
import { useRequestSignal } from '../lib/query'
import { devicePath, request, webhookPatch } from '../lib/api'
import { Button, ErrorNotice, Field, Notice, Spinner } from '../components/ui'
export function WebhookSettings({ device }: { device: Device }) {
  const { t } = useTranslation()
  const [saved, setSaved] = useState(false)
  const data = useQuery({
    queryKey: ['webhook', device.instance_id],
    queryFn: ({ signal }) =>
      request<Webhook>(devicePath(device, '/webhook'), { scope: device, signal }),
    refetchOnWindowFocus: false,
  })
  if (data.isPending || (data.isFetching && !data.isFetchedAfterMount)) return <Spinner />
  return (
    <>
      <ErrorNotice error={data.error} />
      {saved && <Notice success>{t('saved')}</Notice>}
      {data.data && (
        <WebhookForm
          key={`${device.instance_id}:${data.dataUpdatedAt}`}
          device={device}
          config={data.data}
          onSaved={() => setSaved(true)}
          onDirty={() => setSaved(false)}
        />
      )}
    </>
  )
}
function WebhookForm({
  device,
  config,
  onSaved,
  onDirty,
}: {
  device: Device
  config: Webhook
  onSaved(): void
  onDirty(): void
}) {
  const getSignal = useRequestSignal()
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [url, setURL] = useState(config.webhook_url)
  const [events, setEvents] = useState((config.webhook_events ?? []).join('\n'))
  const [replaceSecret, setReplaceSecret] = useState(false)
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const patch = webhookPatch(
    config,
    url,
    events
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean),
    replaceSecret,
    secret,
  )
  async function save(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    onDirty()
    try {
      await request(devicePath(device, '/webhook'), {
        method: 'PATCH',
        body: patch,
        scope: device,
        signal: getSignal(),
      })
      setSecret('')
      setReplaceSecret(false)
      onSaved()
      await cache.invalidateQueries({ queryKey: ['overview'] })
      await cache.invalidateQueries({ queryKey: ['webhook', device.instance_id] })
    } catch (e) {
      setError(e)
      setSecret('')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="two-column">
      <section className="panel form-panel">
        <div className="section-heading">
          <div className="section-icon">
            <WebhookIcon size={21} />
          </div>
          <div>
            <h2>{t('webhook')}</h2>
            <p>{t('webhookHint')}</p>
          </div>
        </div>
        <form onSubmit={save}>
          <Field id="webhook-url" label={t('destination')} hint={t('destinationHint')}>
            <input
              id="webhook-url"
              type="url"
              dir="ltr"
              value={url}
              onChange={(e) => {
                setURL(e.target.value)
                onDirty()
              }}
              placeholder="https://example.com/webhooks/bale"
              autoComplete="off"
              maxLength={4096}
              aria-describedby="webhook-url-hint"
            />
          </Field>
          <Field
            id="webhook-secret-mode"
            label={t('secret')}
            hint={t(config.secret_configured ? 'secretPresent' : 'secretAbsent')}
          >
            <select
              id="webhook-secret-mode"
              value={replaceSecret ? 'replace' : 'keep'}
              onChange={(e) => {
                setReplaceSecret(e.target.value === 'replace')
                setSecret('')
              }}
            >
              <option value="keep">{t('keepSecret')}</option>
              <option value="replace">{t('replaceSecret')}</option>
            </select>
          </Field>
          {replaceSecret && (
            <Field id="webhook-secret" label={t('replaceSecret')} hint={t('secretHint')}>
              <input
                id="webhook-secret"
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                autoComplete="new-password"
                maxLength={4096}
                aria-describedby="webhook-secret-hint"
              />
            </Field>
          )}
          <Field id="webhook-events" label={t('eventFilter')} hint={t('eventsHint')}>
            <textarea
              id="webhook-events"
              dir="ltr"
              rows={4}
              value={events}
              onChange={(e) => setEvents(e.target.value)}
              maxLength={8192}
              placeholder="message"
              aria-describedby="webhook-events-hint"
            />
          </Field>
          <Notice>{t('changesApply')}</Notice>
          <ErrorNotice error={error} />
          <div className="form-footer">
            <Button busy={busy} disabled={!Object.keys(patch).length} type="submit">
              {t('save')}
            </Button>
          </div>
        </form>
      </section>
      <aside className="panel help-panel">
        <LockKeyhole size={25} />
        <h3>{t('effectiveRouting')}</h3>
        <span className="routing-label">{t(config.routing_mode || 'none')}</span>
        {(config.routing_rules ?? []).map((rule, i) => (
          <div className="routing-rule" key={`${rule.source}:${i}`}>
            <strong>{t(rule.source)}</strong>
            <code dir="ltr">{rule.url}</code>
            <small>{rule.events.length ? rule.events.join(', ') : t('allEvents')}</small>
            <small>
              {t('signature')}: {t(rule.secret_configured ? 'enabled' : 'disabled')}
            </small>
          </div>
        ))}
        {!config.routing_rules?.length && <p>{t('none')}</p>}
      </aside>
    </div>
  )
}
