import { useEffect, useState, type FormEvent } from 'react'
import { ClipboardCopy, KeyRound, LoaderCircle, Plus, Smartphone } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Field, FormError } from '@/components/auth-gate'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { permissionLabel } from '@/lib/auth-api'
import { Confirm, EmptyState, LoadError, LoadingRows, Modal } from '@/components/users-shared'
import { errorMessage } from '@/lib/api'
import { authApi, type ApiToken } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { expiryLabel } from '@/lib/companion'
import { formatAge } from '@/lib/format'

function ChangePassword() {
  const { logout } = useAuth()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!current) {
      setError('Enter your current password.')
      return
    }
    if (next.length < 8) {
      setError('Use a new password of at least 8 characters.')
      return
    }
    if (next !== confirm) {
      setError('The new passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await authApi.changePassword({ currentPassword: current, newPassword: next })
      // The server ends every session, so return to the sign-in card with a clear next step.
      await logout('Password updated. Sign in again with your new password.')
    } catch (cause) {
      setError(errorMessage(cause))
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Password</CardTitle>
        <CardDescription>Changing your password signs you out everywhere, including this browser.</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="max-w-md space-y-4">
          <FormError message={error} />
          <Field label="Current password" htmlFor="password-current">
            <Input id="password-current" type="password" value={current} onChange={event => setCurrent(event.target.value)} autoComplete="current-password" required />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="New password" htmlFor="password-new" hint="At least 8 characters.">
              <Input id="password-new" type="password" value={next} onChange={event => setNext(event.target.value)} autoComplete="new-password" required />
            </Field>
            <Field label="Confirm new password" htmlFor="password-confirm">
              <Input id="password-confirm" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" required />
            </Field>
          </div>
          <Button type="submit" disabled={busy}>
            {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {busy ? 'Updating…' : 'Update password'}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

function CreateTokenDialog({ ownPermissions, onClose, onCreated }: {
  ownPermissions: string[]
  onClose: () => void
  onCreated: (token: ApiToken, secret: string) => void
}) {
  const [name, setName] = useState('')
  const [inherit, setInherit] = useState(true)
  const [selected, setSelected] = useState<string[]>([])
  const [expiry, setExpiry] = useState('90')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const choices = [...ownPermissions].sort()

  function toggle(key: string) {
    setSelected(current => (current.includes(key) ? current.filter(item => item !== key) : [...current, key]))
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!name.trim()) {
      setError('Name this token so you can recognize it later.')
      return
    }
    if (!inherit && selected.length === 0) {
      setError('Choose at least one permission, or grant all of your permissions.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const expiresAt = expiry === 'never' ? null : new Date(Date.now() + Number(expiry) * 86_400_000).toISOString()
      // A null grant list means the token inherits the account's current permissions.
      const created = await authApi.createToken({ name: name.trim(), permissions: inherit ? null : selected, expiresAt })
      onCreated(created.token, created.secret)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open
      onOpenChange={next => !next && !busy && onClose()}
      title="Create API token"
      description="Tokens authenticate the native app or automation without your password."
      className="max-w-lg"
    >
      <form onSubmit={submit} className="space-y-4">
        <FormError message={error} />
        <Field label="Name" htmlFor="token-name" hint="For example, the device or script that will use it.">
          <Input id="token-name" value={name} onChange={event => setName(event.target.value)} autoFocus required />
        </Field>
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">Scopes</legend>
          <label className="flex items-start gap-2.5 rounded-md p-1.5 hover:bg-muted/50">
            <input type="radio" name="token-scope-mode" checked={inherit} onChange={() => setInherit(true)} className="mt-0.5 size-4 shrink-0 accent-[var(--primary)]" />
            <span className="text-sm">
              All of my permissions
              <span className="block text-xs text-muted-foreground">
                The token follows this account and cannot grant access you lose later.
              </span>
            </span>
          </label>
          <label className="flex items-start gap-2.5 rounded-md p-1.5 hover:bg-muted/50">
            <input type="radio" name="token-scope-mode" checked={!inherit} onChange={() => setInherit(false)} className="mt-0.5 size-4 shrink-0 accent-[var(--primary)]" />
            <span className="text-sm">Choose permissions</span>
          </label>
          {!inherit && (
            <div className="max-h-56 space-y-1 overflow-y-auto rounded-lg border border-border p-2">
              {choices.map(key => (
                <label key={key} className="flex items-center gap-2.5 rounded-md p-1.5 hover:bg-muted/50">
                  <input
                    type="checkbox"
                    checked={selected.includes(key)}
                    onChange={() => toggle(key)}
                    className="size-4 shrink-0 accent-[var(--primary)]"
                  />
                  <span className="text-sm">{permissionLabel(key)}</span>
                </label>
              ))}
              {choices.length === 0 && <p className="p-1.5 text-sm text-muted-foreground">This account holds no permissions to delegate.</p>}
            </div>
          )}
        </fieldset>
        <Field label="Expiry" htmlFor="token-expiry">
          <select
            id="token-expiry"
            value={expiry}
            onChange={event => setExpiry(event.target.value)}
            className="h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30"
          >
            <option value="30">30 days</option>
            <option value="90">90 days</option>
            <option value="365">1 year</option>
            <option value="never">No expiry</option>
          </select>
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {busy ? 'Creating…' : 'Create token'}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function TokenSecretDialog({ secret, onClose }: { secret: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')

  async function copy() {
    try {
      await navigator.clipboard.writeText(secret)
      setCopied(true)
    } catch {
      setError('Copying was blocked. Select the token and copy it manually.')
    }
  }

  return (
    <Modal
      open
      onOpenChange={next => !next && onClose()}
      title="Copy your API token"
      description="Shown once, at creation."
      className="max-w-lg"
    >
      <div className="space-y-4">
        <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-500">
          Constellarr stores only a hash of this token. If you lose it, revoke the token and create a new one.
        </p>
        <FormError message={error} />
        <div className="flex items-center gap-2">
          <Input readOnly value={secret} className="font-mono text-xs" onFocus={event => event.currentTarget.select()} aria-label="API token secret" />
          <Button variant="outline" size="sm" onClick={() => void copy()}>
            <ClipboardCopy aria-hidden="true" />
            {copied ? 'Copied' : 'Copy'}
          </Button>
        </div>
        <div className="flex justify-end">
          <Button onClick={onClose}>Done</Button>
        </div>
      </div>
    </Modal>
  )
}

function Tokens() {
  const { permissions } = useAuth()
  const [tokens, setTokens] = useState<ApiToken[] | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    void authApi
      .listTokens(controller.signal)
      .then(items => {
        if (active) setTokens(items)
      })
      .catch(cause => {
        if (active && !controller.signal.aborted) setError(errorMessage(cause))
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [attempt])

  return (
    <Card>
      <CardHeader>
        <CardTitle>API tokens</CardTitle>
        <CardDescription>Personal tokens for the native app, scripts, or other automation.</CardDescription>
      </CardHeader>
      <CardContent className="gap-4">
        <div className="flex flex-wrap justify-end gap-2">
          <Button asChild variant="outline" size="sm">
            <a href="#companion">
              <Smartphone aria-hidden="true" />
              Connect the iOS app
            </a>
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus aria-hidden="true" />
            Create token
          </Button>
        </div>
        {error && (
          <LoadError
            message={error}
            onRetry={() => {
              setError('')
              setAttempt(value => value + 1)
            }}
          />
        )}
        {!tokens && !error && <LoadingRows label="Loading tokens…" />}
        {tokens && tokens.length === 0 && (
          <EmptyState title="No API tokens" description="Create one for a device or script that should act as you." />
        )}
        {tokens && tokens.length > 0 && (
          <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
            {tokens.map(token => (
              <li key={token.id} className="flex flex-wrap items-center gap-3 bg-background px-4 py-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium">{token.name}</p>
                  <p className="break-words text-xs text-muted-foreground">
                    {expiryLabel(token.expiresAt)} · {token.lastUsedAt ? `last used ${formatAge(token.lastUsedAt)}` : 'never used'}
                  </p>
                  <p className="break-words text-xs text-muted-foreground">
                    {token.permissions.length
                      ? `${token.permissions.length} ${token.permissions.length === 1 ? 'permission' : 'permissions'}: ${token.permissions.join(', ')}`
                      : 'No permissions'}
                  </p>
                </div>
                <Confirm
                  trigger={
                    <Button variant="ghost" size="sm" className="text-destructive">
                      Revoke
                    </Button>
                  }
                  title="Revoke token"
                  description={`Revoke ${token.name}? Any app using it loses access immediately.`}
                  confirmLabel="Revoke token"
                  onConfirm={async () => {
                    await authApi.revokeToken(token.id)
                    setTokens(list => list?.filter(item => item.id !== token.id) ?? null)
                  }}
                />
              </li>
            ))}
          </ul>
        )}
      </CardContent>

      {creating && (
        <CreateTokenDialog
          ownPermissions={permissions}
          onClose={() => setCreating(false)}
          onCreated={(token, value) => {
            setTokens(list => (list ? [...list, token] : [token]))
            setCreating(false)
            setSecret(value)
          }}
        />
      )}
      {secret && <TokenSecretDialog secret={secret} onClose={() => setSecret('')} />}
    </Card>
  )
}

export function UsersSecurity() {
  const { user, permissions, logout } = useAuth()

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Your account</CardTitle>
          <CardDescription>Signed in as {user?.name}.</CardDescription>
        </CardHeader>
        <CardContent className="gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <p className="font-medium">{user?.name}</p>
            {user?.roles.map(role => (
              <Badge key={role} variant="secondary">
                {role}
              </Badge>
            ))}
          </div>
          <p className="text-sm text-muted-foreground">
            {permissions.length} {permissions.length === 1 ? 'permission' : 'permissions'} from your roles
            {user?.createdAt ? ` · member since ${formatAge(user.createdAt).replace(' ago', '')}` : ''}
          </p>
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <KeyRound className="size-3.5" aria-hidden="true" />
            Your name and roles are managed by an administrator.
          </p>
          <div>
            <Button variant="outline" size="sm" onClick={() => void logout()}>
              Sign out
            </Button>
          </div>
        </CardContent>
      </Card>
      <ChangePassword />
      <Tokens />
    </div>
  )
}
