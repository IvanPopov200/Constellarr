import type { ComponentProps, ReactNode } from 'react'
import { Dialog } from 'radix-ui'
import { CheckCircle2, FilmIcon, LoaderCircle, MusicIcon, RefreshCw, TvIcon, XIcon } from 'lucide-react'
import { cn } from 'cn'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { deliveryPhaseLabel, mediaTypeLabel, requestStatusLabel, type Delivery, type MediaType } from '@/lib/discovery-api'

const statusTones: Record<string, string> = {
  pending: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
  approving: 'border-sky-500/30 bg-sky-500/10 text-sky-300',
  approved: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  available: 'border-emerald-500/40 bg-emerald-500/15 text-emerald-200',
  rejected: 'border-destructive/30 bg-destructive/10 text-destructive',
  cancelled: 'border-border text-muted-foreground',
}

export function RequestStatusBadge({ status }: { status: string }) {
  return (
    <Badge variant="outline" className={cn(statusTones[status] ?? '')}>
      {requestStatusLabel(status)}
    </Badge>
  )
}

export function MediaBadge({ type }: { type: MediaType }) {
  const Icon = type === 'movie' ? FilmIcon : type === 'tv' ? TvIcon : MusicIcon
  return (
    <Badge variant="ghost" className="text-muted-foreground">
      <Icon aria-hidden="true" />
      {mediaTypeLabel(type)}
    </Badge>
  )
}

export function DeliveryProgress({ delivery }: { delivery: Delivery }) {
  const label = deliveryPhaseLabel(delivery.phase)
  const percent = typeof delivery.progress === 'number' ? Math.round(Math.min(1, Math.max(0, delivery.progress)) * 100) : null
  if (!label && percent === null) return null
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span>{label || 'Tracking the library'}</span>
        {percent !== null && <span className="tabular-nums">{percent}%</span>}
        {delivery.total ? (
          <span className="tabular-nums">
            {delivery.done ?? 0}/{delivery.total} episodes
          </span>
        ) : null}
        {delivery.jobId && <span className="font-mono text-[0.7rem]">job {delivery.jobId.slice(0, 8)}</span>}
      </div>
      {percent !== null && (
        <div
          role="progressbar"
          aria-label={`${label || 'progress'}: ${percent}%`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          className="h-1.5 overflow-hidden rounded-full bg-muted"
        >
          <div className={cn('h-full rounded-full', delivery.available ? 'bg-emerald-400' : 'bg-primary')} style={{ width: `${percent}%` }} />
        </div>
      )}
      {delivery.message && !delivery.available && <p className="text-xs text-muted-foreground">{delivery.message}</p>}
    </div>
  )
}

export function NoteBanner({ children, tone = 'success', action }: { children: ReactNode; tone?: 'success' | 'error' | 'info'; action?: ReactNode }) {
  const tones = {
    success: 'border-emerald-500/20 bg-emerald-500/5 text-emerald-400',
    error: 'border-destructive/30 bg-destructive/10 text-destructive',
    info: 'border-border bg-muted/40 text-muted-foreground',
  }
  return (
    <div role={tone === 'error' ? 'alert' : 'status'} className={cn('flex flex-wrap items-center justify-between gap-3 rounded-lg border px-3 py-2 text-sm', tones[tone])}>
      <span className="min-w-0">{children}</span>
      {action}
    </div>
  )
}

export function ErrorNote({ children, onRetry }: { children: ReactNode; onRetry?: () => void }) {
  return (
    <NoteBanner
      tone="error"
      action={
        onRetry && (
          <Button size="sm" variant="outline" onClick={onRetry}>
            <RefreshCw data-icon="inline-start" />
            Retry
          </Button>
        )
      }
    >
      {children}
    </NoteBanner>
  )
}

export function Notice({ children }: { children: ReactNode }) {
  return (
    <NoteBanner tone="success">
      <CheckCircle2 className="mr-2 inline size-4" aria-hidden="true" />
      {children}
    </NoteBanner>
  )
}

export function LoadingNote({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
      <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
      {children}
    </p>
  )
}

export function EmptyNote({ children }: { children: ReactNode }) {
  return <p className="rounded-lg border border-dashed border-border px-4 py-6 text-sm text-muted-foreground">{children}</p>
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="text-sm font-medium">{label}</span>
      {children}
      {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
    </label>
  )
}

export function Choice({ className, children, ...props }: ComponentProps<'select'>) {
  return (
    <select
      className={cn(
        'h-9 rounded-md border border-input bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 dark:bg-input/30',
        className,
      )}
      {...props}
    >
      {children}
    </select>
  )
}

export function Toggle({ id, label, description, checked, disabled, onChange }: {
  id: string
  label: string
  description?: string
  checked: boolean
  disabled?: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-start gap-2.5">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 size-4 shrink-0 accent-primary"
      />
      <div className="space-y-0.5">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
    </div>
  )
}

export function DiscoveryDialog({ active, title, description, children, onClose, size = 'default' }: {
  active: boolean
  title: string
  description: string
  children: ReactNode
  onClose: () => void
  size?: 'default' | 'wide'
}) {
  return (
    <Dialog.Root open={active} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=open]:fade-in-0 motion-reduce:animate-none" />
        <Dialog.Content
          className={cn(
            'fixed top-1/2 left-1/2 z-50 flex max-h-[90vh] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-border bg-card shadow-xl data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 motion-reduce:animate-none',
            size === 'wide' ? 'max-w-3xl' : 'max-w-xl',
          )}
        >
          <header className="flex items-start justify-between gap-4 border-b border-border p-4">
            <div className="min-w-0">
              <Dialog.Title className="font-heading text-base font-semibold">{title}</Dialog.Title>
              <Dialog.Description className="mt-0.5 text-xs text-muted-foreground">{description}</Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <Button variant="ghost" size="icon-sm" aria-label="Close dialog">
                <XIcon />
              </Button>
            </Dialog.Close>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
