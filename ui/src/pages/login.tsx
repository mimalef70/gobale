import { useEffect, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, KeyRound, Smartphone } from 'lucide-react'
import type { Device, LoginState } from '../lib/types'
import { APIError, devicePath, request } from '../lib/api'
import { useRequestSignal, useVisibleInterval } from '../lib/query'
import { Button, ErrorNotice, Field, Notice, Spinner } from '../components/ui'
export function AccountLogin({ device }: { device: Device }) {
  const getSignal = useRequestSignal()
  const { t } = useTranslation()
  const cache = useQueryClient()
  const interval = useVisibleInterval(3000)
  const login = useQuery({
    queryKey: ['login', device.instance_id],
    queryFn: ({ signal }) =>
      request<LoginState>(devicePath(device, '/login'), { scope: device, signal }),
    refetchInterval: (query) =>
      query.state.data?.challenge &&
      ['awaiting_code', 'awaiting_password'].includes(query.state.data.state) &&
      new Date(query.state.data.challenge.expires_at).getTime() >
        new Date(query.state.data.server_time).getTime() + Date.now() - query.state.dataUpdatedAt
        ? interval
        : false,
  })
  const [phone, setPhone] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])
  const challenge = login.data?.challenge
  useEffect(() => {
    setCode('')
    setPassword('')
  }, [challenge?.challenge_id])
  const state = login.data?.state ?? device.status.auth
  const serverNow = login.data
    ? new Date(login.data.server_time).getTime() + (now - login.dataUpdatedAt)
    : now
  const expires = challenge
    ? Math.max(0, Math.ceil((new Date(challenge.expires_at).getTime() - serverNow) / 1000))
    : 0
  const resend = challenge
    ? Math.max(0, Math.ceil((new Date(challenge.resend_available_at).getTime() - serverNow) / 1000))
    : 0
  const isPassword = state === 'awaiting_password' && !!challenge && expires > 0
  const isCode = state === 'awaiting_code' && !!challenge && expires > 0
  const connected = state === 'authenticated'
  async function perform(event: FormEvent, kind: 'phone' | 'code' | 'password') {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    const body =
      kind === 'phone'
        ? { phone: phone.trim() }
        : kind === 'code'
          ? { challenge_id: challenge?.challenge_id, code: code.trim() }
          : { challenge_id: challenge?.challenge_id, password }
    // Clear sensitive input immediately; requests only retain it until completion.
    setCode('')
    setPassword('')
    if (kind === 'phone') setPhone('')
    try {
      await request(devicePath(device, kind === 'phone' ? '/login' : `/login/${kind}`), {
        method: 'POST',
        body,
        scope: device,
        signal: getSignal(),
      })
    } catch (e) {
      if (!(e instanceof APIError && e.code === 'PASSWORD_REQUIRED')) setError(e)
    } finally {
      await Promise.all([
        cache.invalidateQueries({ queryKey: ['login', device.instance_id] }),
        cache.invalidateQueries({ queryKey: ['overview'] }),
      ])
      setBusy(false)
    }
  }
  if (login.isPending) return <Spinner />
  return (
    <div className="two-column">
      <section className="panel form-panel">
        <div className="section-heading">
          <div className="section-icon">
            <KeyRound size={21} />
          </div>
          <div>
            <h2>{t('login')}</h2>
            <p>{t('loginStep', { number: connected || isPassword ? 3 : isCode ? 2 : 1 })}</p>
          </div>
        </div>
        <ErrorNotice error={login.error} />
        <ErrorNotice error={error} />
        {connected ? (
          <div className="success-state">
            <CheckCircle2 size={46} />
            <h3>{t('loginDone')}</h3>
            <p>{t('loginDoneHint')}</p>
          </div>
        ) : (
          <>
            {challenge && expires === 0 && <Notice>{t('codeExpired')}</Notice>}
            {(isCode || isPassword) && (
              <div className="challenge-meta">
                <strong>{t('sentTo', { phone: challenge?.masked_phone })}</strong>
                <span>{t('expiresIn', { seconds: expires })}</span>
              </div>
            )}
            {isCode && (
              <form onSubmit={(event) => void perform(event, 'code')}>
                <Field id="login-code" label={t('code')}>
                  <input
                    id="login-code"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    maxLength={32}
                    dir="ltr"
                    required
                    disabled={busy}
                  />
                </Field>
                <Button type="submit" busy={busy}>
                  {t('verifyCode')}
                </Button>
              </form>
            )}
            {isPassword && (
              <form onSubmit={(event) => void perform(event, 'password')}>
                <Field id="bale-password" label={t('twoFactor')} hint={t('twoFactorHint')}>
                  <input
                    id="bale-password"
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="off"
                    required
                    disabled={busy}
                    aria-describedby="bale-password-hint"
                  />
                </Field>
                <Button type="submit" busy={busy}>
                  {t('verifyPassword')}
                </Button>
              </form>
            )}
            <form
              className={challenge ? 'resend-form' : ''}
              onSubmit={(event) => void perform(event, 'phone')}
            >
              <Field
                id="login-phone"
                label={t('phone')}
                hint={t(challenge ? 'phoneAgain' : 'phoneHint')}
              >
                <input
                  id="login-phone"
                  type="tel"
                  dir="ltr"
                  value={phone}
                  onChange={(e) => setPhone(e.target.value)}
                  autoComplete="off"
                  maxLength={32}
                  required
                  disabled={busy}
                  placeholder="+98…"
                  aria-describedby="login-phone-hint"
                />
              </Field>
              <Button
                type="submit"
                busy={busy}
                disabled={resend > 0}
                variant={challenge ? 'secondary' : 'primary'}
              >
                {t(challenge ? 'resend' : 'requestCode')}
              </Button>
              {resend > 0 && (
                <p className="field-hint" role="status">
                  {t('resendIn', { seconds: resend })}
                </p>
              )}
            </form>
          </>
        )}
      </section>
      <aside className="panel help-panel">
        <Smartphone size={26} />
        <h3>{t('connection')}</h3>
        <dl className="details-list">
          <div>
            <dt>{t('connectionID')}</dt>
            <dd dir="auto">{device.id}</dd>
          </div>
          <div>
            <dt>{t('accountID')}</dt>
            <dd dir="ltr">{device.account_id || '—'}</dd>
          </div>
        </dl>
        <p>{t('secureSession')}</p>
        <p>{t('statusHint')}</p>
      </aside>
    </div>
  )
}
