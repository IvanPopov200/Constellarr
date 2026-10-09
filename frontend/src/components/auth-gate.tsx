import type { ReactNode } from 'react'
import { LoaderCircle, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useAuth } from '@/lib/auth-context'
import { cn } from 'cn'
import { AuthLogin } from '@/components/auth-login'
import { AuthSetup } from '@/components/auth-setup'
import { BrandMark } from '@/components/brand-mark'
export { BrandMark } from '@/components/brand-mark'

export function AuthCard({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <div className="flex min-h-svh items-center justify-center px-4 py-10">
      <div className="w-full max-w-md">
        <div className="mb-6 flex items-center justify-center gap-2.5">
          <BrandMark className="size-7 text-primary" />
          <span className="font-heading text-lg font-semibold tracking-tight">Constellarr</span>
        </div>
        <Card>
          <CardHeader>
            <CardTitle className="text-lg">{title}</CardTitle>
            <CardDescription>{description}</CardDescription>
          </CardHeader>
          <CardContent>{children}</CardContent>
        </Card>
      </div>
    </div>
  )
}

export function AuthGate({ children }: { children: ReactNode }) {
  const { status, error, retry } = useAuth()

  if (status === 'authenticated') return <>{children}</>

  if (status === 'loading') {
    return (
      <div role="status" className="flex min-h-svh flex-col items-center justify-center gap-3 bg-background text-muted-foreground">
        <BrandMark className="size-8 text-primary" />
        <p className="flex items-center gap-2 text-sm">
          <LoaderCircle className="size-4 animate-spin" aria-hidden="true" />
          Checking your session…
        </p>
      </div>
    )
  }

  if (status === 'setup') return <AuthSetup />

  if (status === 'error') {
    return (
      <AuthCard title="Can’t reach Constellarr" description="The server did not answer the session check.">
        <div className="space-y-4">
          <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            {error || 'The backend could not be reached.'}
          </p>
          <Button className="w-full" onClick={retry}>
            <RotateCcw aria-hidden="true" />
            Try again
          </Button>
        </div>
      </AuthCard>
    )
  }

  return <AuthLogin notice={error} />
}

export function Can({
  permission,
  mode = 'all',
  fallback = null,
  children,
}: {
  permission: string | string[]
  mode?: 'all' | 'any'
  fallback?: ReactNode
  children: ReactNode
}) {
  const { can } = useAuth()
  return <>{can(permission, mode) ? children : fallback}</>
}

export function Field({ label, htmlFor, hint, children }: {
  label: string
  htmlFor: string
  hint?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={htmlFor} className="text-sm font-medium">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}

export function FormError({ message, className }: { message: string; className?: string }) {
  if (!message) return null
  return (
    <p role="alert" className={cn('rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive', className)}>
      {message}
    </p>
  )
}
