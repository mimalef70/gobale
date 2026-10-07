import { useTranslation } from 'react-i18next'
import type { WebhookFilter } from '../lib/types'
import { eventDirections, filterDraft, peerTypes, type FilterDraft } from '../lib/webhook-filter'
import { Field } from './ui'

const textFields = [
  ['peers', 'includePeers', 'peerFilterHint'],
  ['exclude_peers', 'excludePeers', 'peerFilterHint'],
  ['sender_ids', 'includeSenders', 'senderFilterHint'],
  ['exclude_sender_ids', 'excludeSenders', 'senderFilterHint'],
] as const

export function WebhookFilterFields({
  draft,
  onChange,
}: {
  draft: FilterDraft
  onChange(draft: FilterDraft): void
}) {
  const { t } = useTranslation()
  return (
    <details className="webhook-filter-editor">
      <summary>{t('deliveryFilters')}</summary>
      <p className="field-hint">{t('deliveryFiltersHint')}</p>
      {textFields.map(([key, label, hint]) => (
        <Field key={key} id={`webhook-${key}`} label={t(label)} hint={t(hint)}>
          <textarea
            id={`webhook-${key}`}
            dir="ltr"
            rows={3}
            value={draft[key]}
            maxLength={4096}
            aria-describedby={`webhook-${key}-hint`}
            placeholder={key.includes('peers') ? 'user:123\ngroup:456' : '123\n456'}
            onChange={(e) => onChange({ ...draft, [key]: e.target.value })}
          />
        </Field>
      ))}
      <fieldset className="webhook-filter-options">
        <legend>{t('peerTypes')}</legend>
        {peerTypes.map((value) => (
          <label key={value}>
            <input
              type="checkbox"
              checked={draft.peer_types.includes(value)}
              onChange={(e) =>
                onChange({
                  ...draft,
                  peer_types: e.target.checked
                    ? [...draft.peer_types, value]
                    : draft.peer_types.filter((item) => item !== value),
                })
              }
            />
            {t(`filterPeer.${value}`)}
          </label>
        ))}
      </fieldset>
      <fieldset className="webhook-filter-options">
        <legend>{t('directions')}</legend>
        {eventDirections.map((value) => (
          <label key={value}>
            <input
              type="checkbox"
              checked={draft.directions.includes(value)}
              onChange={(e) =>
                onChange({
                  ...draft,
                  directions: e.target.checked
                    ? [...draft.directions, value]
                    : draft.directions.filter((item) => item !== value),
                })
              }
            />
            {t(`filterDirection.${value}`)}
          </label>
        ))}
      </fieldset>
      <p className="field-hint">{t('unknownDirectionHint')}</p>
    </details>
  )
}

export function WebhookFilterSummary({ filter }: { filter?: WebhookFilter }) {
  const { t } = useTranslation()
  const draft = filterDraft(filter)
  const entries = textFields.flatMap(([key, label]) =>
    draft[key] ? [[label, draft[key].replaceAll('\n', ', ')]] : [],
  )
  if (draft.peer_types.length)
    entries.push(['peerTypes', draft.peer_types.map((v) => t(`filterPeer.${v}`)).join(', ')])
  if (draft.directions.length)
    entries.push(['directions', draft.directions.map((v) => t(`filterDirection.${v}`)).join(', ')])
  return entries.length ? (
    entries.map(([label, value]) => (
      <small key={label}>
        <strong>{t(label)}: </strong>
        <span dir="auto">{value}</span>
      </small>
    ))
  ) : (
    <small>{t('noDeliveryFilters')}</small>
  )
}
