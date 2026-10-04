import { useState } from 'react'
import type { ComponentProps, ReactNode } from 'react'
import { Dialog } from 'radix-ui'
import { CheckIcon, FilmIcon, LoaderCircleIcon, RefreshCwIcon, XIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { Series } from '@/lib/tv-api'
import { seriesProgress, statusLabel, statusTones, stateKey } from '@/components/tv-shared'
import { cn } from 'cn'

export function Select({ className, children, ...props }: ComponentProps<'select'>) {
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

export function Checkbox({
  id,
  label,
  description,
  checked,
  disabled,
  onChange,
}: {
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

export function Poster({ title, poster, className }: { title: string; poster: string; className?: string }) {
  const [failed, setFailed] = useState(false)
  if (!poster || failed) {
    return (
      <div className={cn('flex items-center justify-center bg-muted', className)}>
        <FilmIcon className="size-7 text-muted-foreground/60" aria-hidden="true" />
      </div>
    )
  }
  return (
    <img
      src={poster}
      alt=""
      title={title || undefined}
      loading="lazy"
      onError={() => setFailed(true)}
      className={cn('object-cover', className)}
    />
  )
}

export function StatusBadge({ status, label }: { status: string; label?: string }) {
  const key = status.trim().toLowerCase().replace(/_/g, '-')
  return (
    <Badge
      variant={key === 'failed' ? 'destructive' : 'outline'}
      className={cn(statusTones[key], key === 'failed' && 'border-destructive/30 bg-destructive/10')}
    >
      {label ?? statusLabel(key)}
    </Badge>
  )
}

export function SeriesStatusBadge({ series }: { series: Series }) {
  return <StatusBadge status={stateKey(series)} />
}

export function Progress({ series }: { series: Series }) {
  const { downloaded, total, wanted, percent } = seriesProgress(series)
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <span className="tabular-nums">
          {downloaded}/{total}
        </span>
        <span>episodes</span>
        {wanted > 0 && <span className="tabular-nums text-amber-300">· {wanted} wanted</span>}
      </div>
      <div
        role="progressbar"
        aria-label={`${downloaded} of ${total} episodes downloaded`}
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={downloaded}
        className="h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <div
          className={cn('h-full rounded-full', percent === null ? 'bg-muted-foreground/40' : 'bg-primary')}
          style={{ width: `${percent ?? 0}%` }}
        />
      </div>
    </div>
  )
}

export function Section({
  title,
  action,
  children,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
}) {
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

export function EmptyState({ children }: { children: ReactNode }) {
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
      <span className="min-w-0">{children}</span>
      {onRetry && (
        <Button size="sm" variant="outline" onClick={onRetry}>
          <RefreshCwIcon data-icon="inline-start" />
          Retry
        </Button>
      )}
    </div>
  )
}

export function Notice({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-400">
      <CheckIcon className="mr-2 inline size-4" />
      {children}
    </p>
  )
}

export function LoadingNote({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
      <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
      {children}
    </p>
  )
}

export function MetaRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
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
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 motion-reduce:animate-none" />
        <Dialog.Content className="fixed top-1/2 left-1/2 z-50 flex max-h-[90vh] w-[calc(100vw-2rem)] max-w-5xl -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-border bg-card shadow-xl data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 motion-reduce:animate-none">
          <header className="flex items-start justify-between gap-4 border-b border-border p-4">
            <div className="min-w-0">
              <Dialog.Title className="font-heading text-base font-semibold">{title}</Dialog.Title>
              <Dialog.Description className="mt-0.5 text-xs text-muted-foreground">
                {description}
              </Dialog.Description>
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
