import { useEffect, useState, type FormEvent } from 'react'
import { HashRouter, NavLink, Navigate, Route, Routes, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  BookOpen,
  ChevronRight,
  LogOut,
  Radio,
  Server,
  ShieldCheck,
  Smartphone,
  Webhook,
} from 'lucide-react'
import { apiEvents, APIError, request, setSession } from './lib/api'
import { useRequestSignal, useVisibleInterval } from './lib/query'
import type { Overview, Scope, Session } from './lib/types'
import { Brand, Preferences } from './components/preferences'
import { Button, ErrorNotice, Field, Notice, Spinner } from './components/ui'
import { Accounts } from './pages/accounts'
import { DevicePage } from './pages/device'
import { Deliveries } from './pages/deliveries'
import { ServicePage } from './pages/service'
export default function App() {
  return (
    <HashRouter>
      <Authentication />
    </HashRouter>
  )
}
function Authentication() {
  const cache = useQueryClient()
  const [session, setAuth] = useState<Session | null | undefined>(undefined)
  const [initialError, setInitialError] = useState<unknown>()
  const [expired, setExpired] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    void request<Session>('auth/session', { auth: true, signal: controller.signal })
      .then((value) => {
        setSession(value)
        setAuth(value)
      })
      .catch((error) => {
        if (controller.signal.aborted) return
        if (!(error instanceof APIError && error.status === 401)) setInitialError(error)
        setAuth(null)
      })
    return () => controller.abort()
  }, [])
  useEffect(() => {
    const clear = () => {
      setSession()
      setAuth((previous) => {
        if (previous) setExpired(true)
        return null
      })
      void cache.cancelQueries()
      cache.clear()
    }
    apiEvents.addEventListener('unauthorized', clear)
    return () => apiEvents.removeEventListener('unauthorized', clear)
  }, [cache])
  async function logout() {
    await request('auth/session', { method: 'DELETE', auth: true })
    setSession()
    await cache.cancelQueries()
    cache.clear()
    setAuth(null)
    setExpired(false)
  }
  if (session === undefined)
    return (
      <div className="boot">
        <Brand />
        <Spinner />
      </div>
    )
  return session ? (
    <Shell logout={logout} />
  ) : (
    <LoginPanel
      expired={expired}
      initialError={initialError}
      success={(value) => {
        setSession(value)
        setAuth(value)
        setExpired(false)
        setInitialError(undefined)
      }}
    />
  )
}
function LoginPanel({
  success,
  expired,
  initialError,
}: {
  success(value: Session): void
  expired: boolean
  initialError: unknown
}) {
  const { t } = useTranslation()
  const getSignal = useRequestSignal()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(initialError)
  async function login(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    const body = { username, password }
    setPassword('')
    try {
      success(
        await request<Session>('auth/session', {
          method: 'POST',
          auth: true,
          body,
          signal: getSignal(),
        }),
      )
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <main className="login-layout">
      <section className="login-story">
        <Brand />
        <div className="login-intro">
          <div className="micro-label">
            <Radio size={15} />
            BALE CONNECTION GATEWAY
          </div>
          <h1>{t('gateway')}</h1>
          <p>{t('gatewayDetail')}</p>
          <div className="connection-graphic" aria-hidden>
            <div className="graphic-core">
              <Webhook size={36} />
            </div>
            <div className="graphic-node node-a">
              <Smartphone />
            </div>
            <div className="graphic-node node-b">
              <ShieldCheck />
            </div>
            <div className="graphic-node node-c">
              <Server />
            </div>
            <i />
            <i />
            <i />
          </div>
        </div>
        <small>GoOmni · REST API · Native Go</small>
      </section>
      <section className="login-side">
        <div className="login-preferences">
          <Preferences />
        </div>
        <div className="login-form">
          <p className="eyebrow">{t('adminAccess')}</p>
          <h2>{t('welcome')}</h2>
          <p className="login-hint">{t('signInHint')}</p>
          {expired && <Notice>{t('expiredSession')}</Notice>}
          <form onSubmit={login}>
            <Field id="admin-username" label={t('username')}>
              <input
                id="admin-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                dir="ltr"
                required
                disabled={busy}
                maxLength={256}
              />
            </Field>
            <Field id="admin-password" label={t('password')}>
              <input
                id="admin-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                required
                disabled={busy}
                maxLength={4096}
              />
            </Field>
            <ErrorNotice error={error} />
            <Button type="submit" busy={busy} className="full-width">
              {t('signIn')}
              <ChevronRight size={17} />
            </Button>
          </form>
          <div className="credential-note">
            <ShieldCheck size={15} />
            {t('secureSession')}
          </div>
        </div>
        <footer className="login-footer">
          <a href="https://mimalef70.github.io/goomni/" target="_blank" rel="noreferrer">
            {t('docs')}
          </a>
          <span>GoOmni</span>
        </footer>
      </section>
    </main>
  )
}
function Shell({ logout }: { logout(): Promise<void> }) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const navigate = useNavigate()
  const interval = useVisibleInterval(10_000)
  const overview = useQuery({
    queryKey: ['overview'],
    queryFn: ({ signal }) => request<Overview>('devices/overview', { signal }),
    refetchInterval: interval,
  })
  const [changed, setChanged] = useState(false)
  const [signoutBusy, setSignoutBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  useEffect(() => {
    const listener = (event: Event) => {
      const scope = (event as CustomEvent<Scope>).detail
      void cache.cancelQueries({
        predicate: (q) => !!scope && q.queryKey.includes(scope.instance_id),
      })
      cache.removeQueries({ predicate: (q) => !!scope && q.queryKey.includes(scope.instance_id) })
      void cache.invalidateQueries({ queryKey: ['overview'] })
      setChanged(true)
      navigate('/accounts')
    }
    apiEvents.addEventListener('instance-changed', listener)
    return () => apiEvents.removeEventListener('instance-changed', listener)
  }, [cache, navigate])
  // Remove retired account data after every snapshot, even if a tab was open when
  // another operator deleted/replaced its alias. No old cache reaches a new ID.
  useEffect(() => {
    if (!overview.data) return
    const instances = new Set(overview.data.devices.map((d) => d.instance_id))
    const scoped = new Set(['login', 'webhook', 'deliveries', 'delivery'])
    const gone = (key: readonly unknown[]) =>
      scoped.has(String(key[0])) && !instances.has(String(key[1]))
    void cache.cancelQueries({ predicate: (q) => gone(q.queryKey) })
    cache.removeQueries({ predicate: (q) => gone(q.queryKey) })
  }, [cache, overview.data])
  async function signout() {
    setSignoutBusy(true)
    setError(undefined)
    try {
      await logout()
    } catch (e) {
      setError(e)
    } finally {
      setSignoutBusy(false)
    }
  }
  return (
    <div className="app-layout">
      <aside className="sidebar">
        <NavLink className="brand-link" to="/accounts">
          <Brand />
        </NavLink>
        <div className="sidebar-caption">{t('admin')}</div>
        <nav className="main-nav">
          {[
            { path: '/accounts', label: 'accounts', Icon: Smartphone },
            { path: '/deliveries', label: 'deliveries', Icon: Webhook },
            { path: '/service', label: 'service', Icon: Server },
          ].map(({ path, label, Icon }) => (
            <NavLink to={path} key={path} onClick={() => setChanged(false)}>
              <Icon size={19} />
              <span>{t(label)}</span>
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-footer">
          <a href="https://mimalef70.github.io/goomni/" target="_blank" rel="noreferrer">
            <BookOpen size={17} />
            {t('docs')}
          </a>
          <div className="sidebar-server">
            <span className="server-indicator" />
            {window.location.host}
          </div>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <div className="topbar-title">
            <span className="micro-mark" />
            {t('admin')}
          </div>
          <div className="topbar-actions">
            <Preferences />
            <span className="topbar-divider" />
            <Button
              variant="ghost"
              busy={signoutBusy}
              aria-label={t('signOut')}
              onClick={() => void signout()}
            >
              <LogOut size={16} />
              <span>{t('signOut')}</span>
            </Button>
          </div>
        </header>
        <main className="main-content" id="main-content">
          <ErrorNotice error={error || overview.error} />
          {changed && <Notice>{t('changed')}</Notice>}
          {overview.isPending ? (
            <Spinner />
          ) : overview.data ? (
            <Routes>
              <Route path="/accounts" element={<Accounts overview={overview.data} />} />
              <Route path="/accounts/:id/:tab" element={<DevicePage overview={overview.data} />} />
              <Route path="/deliveries" element={<Deliveries devices={overview.data.devices} />} />
              <Route
                path="/service"
                element={<ServicePage serverTime={overview.data.server_time} />}
              />
              <Route path="*" element={<Navigate to="/accounts" replace />} />
            </Routes>
          ) : (
            <Button variant="secondary" onClick={() => void overview.refetch()}>
              {t('tryAgain')}
            </Button>
          )}
        </main>
        <footer className="workspace-footer">
          <span dir="ltr">GoOmni</span>
          <span>{t('admin')}</span>
        </footer>
      </div>
    </div>
  )
}
