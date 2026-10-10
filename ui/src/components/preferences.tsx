import { Languages, Moon, Sun } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
function initialTheme(): 'light' | 'dark' {
  try {
    const stored = localStorage.getItem('goomni.theme')
    if (stored === 'light' || stored === 'dark') return stored
  } catch {
    /* Storage can be unavailable. */
  }
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}
export function Preferences() {
  const { t, i18n } = useTranslation()
  const [theme, setTheme] = useState(initialTheme)
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try {
      localStorage.setItem('goomni.theme', theme)
    } catch {
      /* Preferences are optional. */
    }
  }, [theme])
  useEffect(() => {
    document.documentElement.lang = i18n.language
    document.documentElement.dir = i18n.language === 'fa' ? 'rtl' : 'ltr'
    try {
      localStorage.setItem('goomni.language', i18n.language)
    } catch {
      /* Preferences are optional. */
    }
  }, [i18n.language])
  return (
    <div className="preferences">
      <button
        className="icon-button"
        title={t('language')}
        aria-label={t('language')}
        onClick={() => void i18n.changeLanguage(i18n.language === 'fa' ? 'en' : 'fa')}
      >
        <Languages size={18} />
      </button>
      <button
        className="icon-button"
        title={t('theme')}
        aria-label={t('theme')}
        onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
      >
        {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
      </button>
    </div>
  )
}
export function Brand() {
  return (
    <div className="brand">
      <svg width="35" height="38" viewBox="0 0 44 46" aria-hidden>
        <path
          d="M8 1h28a7 7 0 0 1 7 7v22a7 7 0 0 1-7 7H20L10 45v-8H8a7 7 0 0 1-7-7V8a7 7 0 0 1 7-7Z"
          fill="currentColor"
        />
        <path
          d="M22 9a11 11 0 1 0 0 22 11 11 0 0 0 0-22Zm0 5a6 6 0 1 1 0 12 6 6 0 0 1 0-12Z"
          className="brand-letter"
        />
      </svg>
      <span dir="ltr">GoOmni</span>
    </div>
  )
}
