/**
 * A deliberately small i18n layer: two dictionaries, a `t()` with {name}
 * interpolation, and the document direction. A full library would add weight
 * for features this app does not use (plural rules beyond one/other, ICU).
 */

import { useEffect } from 'react'

import { usePrefs, type Language } from '../stores/prefs'
import { en, type Messages } from './en'
import { fa } from './fa'

export type Key = keyof Messages

const DICTS: Record<Language, Messages> = { en, fa }

export function translate(lang: Language, key: Key, vars?: Record<string, string | number>): string {
  const raw = DICTS[lang][key] ?? en[key] ?? key
  if (!vars) return raw
  return raw.replace(/\{(\w+)\}/g, (_, name: string) => (name in vars ? String(vars[name]) : `{${name}}`))
}

/** Returns a translator bound to the current language. */
export function useT(): (key: Key, vars?: Record<string, string | number>) => string {
  const lang = usePrefs((s) => s.language)
  return (key, vars) => translate(lang, key, vars)
}

export function isRtl(lang: Language): boolean {
  return lang === 'fa'
}

/** Keeps <html dir/lang> in step with the chosen language. */
export function useDocumentDirection(): void {
  const lang = usePrefs((s) => s.language)
  useEffect(() => {
    const root = document.documentElement
    root.lang = lang
    root.dir = isRtl(lang) ? 'rtl' : 'ltr'
  }, [lang])
}

/** Formats a number with the language's digits. */
export function useNumber(): (n: number, digits?: number) => string {
  const lang = usePrefs((s) => s.language)
  return (n, digits = 0) =>
    n.toLocaleString(lang === 'fa' ? 'fa-IR' : 'en-US', {
      maximumFractionDigits: digits,
      minimumFractionDigits: 0,
    })
}
