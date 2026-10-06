/* eslint-disable react-refresh/only-export-components -- shared subtitle labels and helpers live beside the shared subtitle components. */
import type { ReactNode } from 'react'
import { CheckIcon, ChevronDownIcon, XIcon } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { Dialog } from 'radix-ui'
import { cn } from 'cn'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { SubtitleSidecar, SubtitleVideo, SubtitleWanted } from '@/lib/subtitles-api'

type Variant = Pick<SubtitleSidecar | SubtitleWanted, 'language' | 'forced' | 'hi'>

export function videoTitle(video: Pick<SubtitleVideo, 'title' | 'seriesTitle'>) {
  return video.seriesTitle ? `${video.seriesTitle} — ${video.title}` : video.title
}

export function videoMeta(video: Pick<SubtitleVideo, 'year' | 'kind' | 'season' | 'episode'>) {
  const position = video.kind === 'episode' ? `S${video.season ?? 0}E${video.episode ?? 0}` : 'Movie'
  return [video.year > 0 ? String(video.year) : '', position].filter(Boolean).join(' · ')
}

export function variantText(item: Variant) {
  return languageName(item.language) + (item.forced ? ' (forced)' : '') + (item.hi ? ' (hearing impaired)' : '')
}

const sourceLabels: Record<string, string> = {
  sync: 'Timing adjusted',
  embedded: 'Extracted from video',
  review: 'Saved after review',
  scan: 'Found on disk',
}

export function sourceLabel(source: string) {
  if (source.startsWith('provider:')) return 'Downloaded'
  return sourceLabels[source] ?? source
}

const jobKindLabels: Record<string, string> = {
  download: 'Downloading',
  sync: 'Adjusting timing',
  translate: 'Translating',
  extract: 'Extracting from video',
}

export function jobKindLabel(kind: string) {
  return jobKindLabels[kind] ?? kind
}

const historyActionLabels: Record<string, string> = {
  downloaded: 'Downloaded',
  'download-failed': 'Download failed',
  queued: 'Queued',
  synced: 'Timing adjusted',
  translated: 'Translated',
  extracted: 'Extracted',
  applied: 'Saved',
  discarded: 'Discarded',
}

export function historyActionLabel(action: string) {
  return historyActionLabels[action] ?? action
}

export const commonLanguages = [
  'en', 'de', 'fr', 'es', 'it', 'pt-BR', 'nl', 'pl', 'ru', 'uk', 'sv', 'no', 'da', 'fi', 'cs', 'tr', 'ar', 'he', 'ja', 'ko', 'zh', 'hi',
]

let displayNames: Intl.DisplayNames | null | undefined

// Language names come from the browser so codes such as pt-BR stay readable.
export function languageName(code: string | null | undefined) {
  const value = (code ?? '').trim()
  if (!value) return 'Unknown language'
  if (displayNames === undefined) {
    try {
      displayNames = new Intl.DisplayNames(['en'], { type: 'language' })
    } catch {
      displayNames = null
    }
  }
  try {
    return displayNames?.of(value) || value
  } catch {
    return value
  }
}

// Extra codes stay selectable so existing profiles keep working.
export function languageOptions(extra: string[] = []) {
  const codes = [...new Set([...extra.map((code) => code.trim()).filter(Boolean), ...commonLanguages])]
  return codes
    .map((code) => ({ code, name: languageName(code) }))
    .sort((left, right) => left.name.localeCompare(right.name))
}

export function Select({ className = '', children, ...props }: React.ComponentProps<'select'>) {
  return (
    <select
      className={
        'h-9 rounded-md border border-input bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 dark:bg-input/30 ' +
        className
      }
      {...props}
    >
      {children}
    </select>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex min-w-0 flex-col gap-1 text-sm">
      <span className="font-medium">{label}</span>
      {children}
      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
    </label>
  )
}

export function Toggle({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean
  onChange: (value: boolean) => void
  label: string
  hint?: string
  disabled?: boolean
}) {
  return (
    <label className="flex items-start gap-2.5 text-sm">
      <input
        type="checkbox"
        className="mt-0.5 size-4 shrink-0 accent-primary"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="min-w-0">
        <span className="font-medium">{label}</span>
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
      </span>
    </label>
  )
}

export function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-heading text-sm font-semibold">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  )
}

export function EmptyNote({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-border px-4 py-6 text-sm text-muted-foreground">
      {children}
    </div>
  )
}

export function ErrorNote({ children, onRetry }: { children: ReactNode; onRetry?: () => void }) {
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
    >
      <span className="min-w-0 break-words">{children}</span>
      {onRetry && (
        <Button size="sm" variant="outline" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  )
}

// Short feedback that sits next to the control it belongs to.
export function ActionNote({
  tone = 'muted',
  id,
  children,
}: {
  tone?: 'muted' | 'success' | 'warning' | 'error'
  id?: string
  children: ReactNode
}) {
  if (tone === 'success') {
    return (
      <p
        id={id}
        role="status"
        className="flex items-center gap-1.5 rounded-md border border-emerald-400/30 bg-emerald-400/10 px-2 py-1 text-xs text-emerald-300"
      >
        <CheckIcon className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="min-w-0 break-words">{children}</span>
      </p>
    )
  }
  return (
    <p
      id={id}
      role={tone === 'error' ? 'alert' : undefined}
      className={cn(
        'break-words text-xs',
        tone === 'error' ? 'text-destructive' : tone === 'warning' ? 'text-amber-500' : 'text-muted-foreground',
      )}
    >
      {children}
    </p>
  )
}

// Secondary detail stays behind a disclosure so primary rows stay readable.
export function Disclosure({
  label,
  detail,
  variant = 'card',
  children,
}: {
  label: string
  detail?: ReactNode
  variant?: 'card' | 'inline'
  children: ReactNode
}) {
  const inline = variant === 'inline'
  return (
    <details className={cn('group min-w-0', !inline && 'rounded-lg border border-border')}>
      <summary
        className={cn(
          'flex cursor-pointer list-none items-center gap-2 [&::-webkit-details-marker]:hidden',
          inline ? 'text-xs text-muted-foreground hover:text-foreground' : 'p-3 text-sm',
        )}
      >
        <ChevronDownIcon className="size-3.5 shrink-0 transition-transform group-open:rotate-180" aria-hidden="true" />
        <span className={cn('min-w-0', !inline && 'font-medium')}>{label}</span>
        {detail && <span className="ml-auto text-xs text-muted-foreground">{detail}</span>}
      </summary>
      <div className={cn('min-w-0', inline ? 'mt-2 space-y-2 text-xs text-muted-foreground' : 'space-y-3 border-t border-border p-3')}>
        {children}
      </div>
    </details>
  )
}

export function TabButtons<T extends string>({
  items,
  value,
  onChange,
  label,
  className,
}: {
  items: { value: T; label: string; icon?: LucideIcon; count?: number }[]
  value: T
  onChange: (value: T) => void
  label: string
  className?: string
}) {
  return (
    <div role="group" aria-label={label} className={cn('flex flex-wrap gap-1 rounded-lg border border-border p-1', className)}>
      {items.map((item) => {
        const Icon = item.icon
        return (
          <Button
            key={item.value}
            size="sm"
            variant={value === item.value ? 'secondary' : 'ghost'}
            onClick={() => onChange(item.value)}
            aria-pressed={value === item.value}
          >
            {Icon && <Icon data-icon="inline-start" />}
            {item.label}
            {(item.count ?? 0) > 0 && <Badge variant="outline">{item.count}</Badge>}
          </Button>
        )
      })}
    </div>
  )
}

export function DialogShell({
  active,
  title,
  description,
  children,
  onClose,
}: {
  active: boolean
  title: string
  description: string
  children: ReactNode
  onClose: () => void
}) {
  return (
    <Dialog.Root open={active} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60" />
        <Dialog.Content className="fixed top-1/2 left-1/2 z-50 flex max-h-[92vh] w-[calc(100vw-1.25rem)] max-w-4xl -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-border bg-card shadow-xl sm:w-[calc(100vw-2rem)]">
          <header className="flex items-start justify-between gap-3 border-b border-border p-3 sm:p-4">
            <div className="min-w-0">
              <Dialog.Title className="font-heading text-base font-semibold break-words">{title}</Dialog.Title>
              <Dialog.Description className="mt-0.5 break-words text-xs text-muted-foreground">
                {description}
              </Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <Button variant="ghost" size="icon-sm" aria-label="Close dialog">
                <XIcon />
              </Button>
            </Dialog.Close>
          </header>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto overscroll-contain p-3 sm:p-4">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

export function VariantBadges({ item }: { item: Pick<SubtitleSidecar | SubtitleWanted, 'language' | 'forced' | 'hi'> }) {
  return (
    <span className="flex flex-wrap items-center gap-1">
      <Badge variant="outline" title={item.language || undefined}>
        {languageName(item.language)}
      </Badge>
      {item.forced && (
        <Badge variant="secondary" title="Forced subtitles translate foreign dialogue only">
          forced
        </Badge>
      )}
      {item.hi && (
        <Badge variant="secondary" title="Hearing-impaired subtitles describe sounds and speakers">
          hearing impaired
        </Badge>
      )}
    </span>
  )
}
