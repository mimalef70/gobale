import * as Dialog from '@radix-ui/react-dialog'
import { AlertCircle, Check, LoaderCircle, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { ButtonHTMLAttributes, PropsWithChildren, ReactNode } from 'react'
import { APIError } from '../lib/api'

export function Button({
  children,
  variant = 'primary',
  busy,
  className = '',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  busy?: boolean
}) {
  return (
    <button
      {...props}
      disabled={props.disabled || busy}
      className={`button ${variant} ${className}`}
      aria-busy={busy || undefined}
    >
      {busy && <LoaderCircle className="spin" size={16} aria-hidden />}
      {children}
    </button>
  )
}
export function Modal({
  open,
  onOpenChange,
  title,
  description,
  children,
}: PropsWithChildren<{
  open: boolean
  onOpenChange(open: boolean): void
  title: string
  description?: string
}>) {
  const { t } = useTranslation()
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content"
          {...(!description ? { 'aria-describedby': undefined } : {})}
        >
          <div className="dialog-heading">
            <div>
              <Dialog.Title>{title}</Dialog.Title>
              {description && <Dialog.Description>{description}</Dialog.Description>}
            </div>
            <Dialog.Close asChild>
              <button className="icon-button" aria-label={t('close')}>
                <X size={18} />
              </button>
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
export function ErrorNotice({ error }: { error: unknown }) {
  const { t } = useTranslation()
  if (!error) return null
  const code = error instanceof APIError ? error.code : 'REQUEST_FAILED'
  const actionable: Record<string, string> = {
    AUTH_REQUIRED: 'errors.accountAuth',
    INVALID_CODE: 'errors.invalidCode',
    AUTH_CODE_INVALID: 'errors.invalidCode',
    INVALID_PASSWORD: 'errors.invalidPassword',
    AUTH_PASSWORD_INVALID: 'errors.invalidPassword',
    CHALLENGE_EXPIRED: 'errors.expiredCode',
    AUTH_RESEND_TOO_SOON: 'errors.cooldown',
    WEBHOOK_SECRET_REQUIRED: 'errors.webhookSecret',
    INVALID_WEBHOOK_FILTER: 'invalidWebhookFilter',
    UI_LOGIN_RATE_LIMITED: 'errors.rateLimited',
    INVALID_PHONE: 'errors.invalidPhone',
    DELIVERY_CONFLICT: 'errors.conflict',
  }
  const label =
    actionable[code] ??
    (code === 'NETWORK_ERROR'
      ? 'networkError'
      : ['UNAUTHORIZED', 'UI_UNAUTHORIZED', 'UI_INVALID_CREDENTIALS'].includes(code)
        ? 'authError'
        : code === 'DEVICE_INSTANCE_CHANGED'
          ? 'changed'
          : 'genericError')
  return (
    <div className="notice danger-notice" role="alert">
      <AlertCircle size={18} aria-hidden />
      <div>
        <strong>{t(label)}</strong>
        <code dir="ltr">{code}</code>
        {error instanceof APIError && error.retryAfter ? (
          <span>{t('resendIn', { seconds: error.retryAfter })}</span>
        ) : null}
      </div>
    </div>
  )
}
export function Notice({ children, success = false }: PropsWithChildren<{ success?: boolean }>) {
  return (
    <div
      className={`notice ${success ? 'success-notice' : ''}`}
      role={success ? 'status' : undefined}
    >
      {success ? <Check size={18} aria-hidden /> : <AlertCircle size={18} aria-hidden />}
      <div>{children}</div>
    </div>
  )
}
export function Spinner() {
  const { t } = useTranslation()
  return (
    <div className="loading" role="status">
      <LoaderCircle size={22} className="spin" aria-hidden />
      <span>{t('loading')}</span>
    </div>
  )
}
export function Empty({
  icon,
  title,
  children,
}: PropsWithChildren<{ icon: ReactNode; title: string }>) {
  return (
    <div className="empty">
      <div className="empty-icon">{icon}</div>
      <h3>{title}</h3>
      {children}
    </div>
  )
}
export function Status({ value }: { value?: string }) {
  const { t } = useTranslation()
  const state = value || 'unknown'
  const good = ['authenticated', 'connected', 'current', 'delivered', 'ready'].includes(state)
  const bad = ['failed', 'gap_detected', 'degraded'].includes(state)
  const wait = [
    'connecting',
    'recovering',
    'awaiting_code',
    'awaiting_password',
    'pending',
    'sending',
    'paused',
    'queued',
    'retry',
    'delivering',
  ].includes(state)
  return (
    <span className={`status ${good ? 'good' : bad ? 'bad' : wait ? 'wait' : 'neutral'}`}>
      <i aria-hidden />
      {t(`status.${state}`, { defaultValue: state })}
    </span>
  )
}
export function Field({
  label,
  id,
  hint,
  children,
}: PropsWithChildren<{ label: string; id: string; hint?: string }>) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children}
      {hint && (
        <p id={`${id}-hint`} className="field-hint">
          {hint}
        </p>
      )}
    </div>
  )
}
