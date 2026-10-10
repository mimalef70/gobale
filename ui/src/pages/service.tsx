import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { BookOpen, ExternalLink, ShieldCheck } from 'lucide-react'
import { formatDate } from '../lib/query'
import { request } from '../lib/api'
import type { Info, Provider } from '../lib/types'
import { ErrorNotice, Spinner, Status } from '../components/ui'
export function ServicePage({ serverTime }: { serverTime: string }) {
  const { t, i18n } = useTranslation()
  const info = useQuery({
    queryKey: ['info'],
    queryFn: ({ signal }) => request<Info>('app/info', { signal }),
  })
  const providers = useQuery({
    queryKey: ['providers'],
    queryFn: ({ signal }) => request<Provider[]>('app/providers', { signal }),
  })
  const ready = useQuery({
    queryKey: ['ready'],
    queryFn: ({ signal }) => request<{ status: string }>('ready', { signal }),
  })
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">GoOmni</p>
          <h1>{t('service')}</h1>
          <p>{t('serviceHint')}</p>
        </div>
      </div>
      <div className="two-column">
        <section className="panel form-panel">
          <div className="section-heading">
            <div className="section-icon">
              <ShieldCheck size={22} />
            </div>
            <div>
              <h2>{t('independence')}</h2>
              <p>{t('independenceHint')}</p>
            </div>
          </div>
          <ErrorNotice error={info.error || ready.error} />
          {info.isPending ? (
            <Spinner />
          ) : (
            <dl className="details-list">
              <div>
                <dt>{t('version')}</dt>
                <dd dir="ltr">{info.data?.version || '—'}</dd>
              </div>
              <div>
                <dt>{t('stage')}</dt>
                <dd>{info.data?.release_stage || '—'}</dd>
              </div>
              <div>
                <dt>{t('serverTime')}</dt>
                <dd>{formatDate(serverTime, i18n.language)}</dd>
              </div>
              <div>
                <dt>{t('readiness')}</dt>
                <dd>
                  {ready.isPending ? (
                    t('loading')
                  ) : ready.data ? (
                    <Status value="ready" />
                  ) : (
                    t('unavailable')
                  )}
                </dd>
              </div>
            </dl>
          )}
          <p className="muted small">{t('settingsHint')}</p>
        </section>
        <aside className="panel help-panel">
          <BookOpen size={26} />
          <h3>{t('docs')}</h3>
          <p>{t('apiDocsHint')}</p>
          <a
            className="button secondary"
            href="https://mimalef70.github.io/goomni/"
            target="_blank"
            rel="noreferrer"
          >
            {t('docs')}
            <ExternalLink size={16} />
          </a>
        </aside>
      </div>
      <section className="panel form-panel">
        <h2>{t('providers')}</h2>
        <ErrorNotice error={providers.error} />
        {providers.isPending ? (
          <Spinner />
        ) : (
          <dl className="details-list">
            {providers.data?.map((provider) => (
              <div key={provider.id}>
                <dt>{t(`providerName.${provider.id}`)}</dt>
                <dd>
                  {provider.enabled ? t('available') : t('providerUnavailable')}
                  <small className="cell-hint">{provider.verification}</small>
                </dd>
              </div>
            ))}
          </dl>
        )}
      </section>
      <p className="session-note">{t('sessionHint')}</p>
    </>
  )
}
