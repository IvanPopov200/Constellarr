import { useEffect, useState, type FormEvent } from 'react'
import { Check, LoaderCircle, UserPlus } from 'lucide-react'
import { Field, FormError } from '@/components/auth-gate'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Confirm, EmptyState, LoadError, LoadingRows, Modal } from '@/components/users-shared'
import { errorMessage } from '@/lib/api'
import { authApi, type AuthUser, type Role } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { formatAge } from '@/lib/format'

function roleIdsFor(person: AuthUser, roles: Role[]) {
  const byName = new Map(roles.map(role => [role.name.toLowerCase(), role.id]))
  return person.roles
    .map(name => byName.get(name.toLowerCase()))
    .filter((id): id is string => Boolean(id))
}

function RolePicker({ roles, selected, onChange }: {
  roles: Role[]
  selected: string[]
  onChange: (next: string[]) => void
}) {
  function toggle(id: string) {
    onChange(selected.includes(id) ? selected.filter(item => item !== id) : [...selected, id])
  }

  return (
    <div className="space-y-1 rounded-lg border border-border p-2">
      {roles.map(role => (
        <label key={role.id} className="flex items-center gap-2.5 rounded-md p-1.5 hover:bg-muted/50">
          <input
            type="checkbox"
            checked={selected.includes(role.id)}
            onChange={() => toggle(role.id)}
            className="size-4 shrink-0 accent-[var(--primary)]"
          />
          <span className="text-sm">{role.name}</span>
          {role.builtin && <Badge variant="secondary">Built-in</Badge>}
        </label>
      ))}
      {roles.length === 0 && <p className="p-1.5 text-sm text-muted-foreground">No roles are available.</p>}
    </div>
  )
}

function CreateUserDialog({ open, onOpenChange, roles, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  roles: Role[]
  onCreated: (user: AuthUser) => void
}) {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (name.trim().length < 2) {
      setError('Choose a name of at least 2 characters.')
      return
    }
    if (password.length < 8) {
      setError('Use a password of at least 8 characters.')
      return
    }
    setBusy(true)
    setError('')
    try {
      onCreated(await authApi.createUser({ name: name.trim(), password, active: true, roles: selected }))
      setName('')
      setPassword('')
      setSelected([])
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onOpenChange={onOpenChange} title="Create account" description="The account can sign in immediately with this password.">
      <form onSubmit={submit} className="space-y-4">
        <FormError message={error} />
        <Field label="Name" htmlFor="new-user-name" hint="Used to sign in and shown beside their activity.">
          <Input id="new-user-name" value={name} onChange={event => setName(event.target.value)} autoComplete="off" required />
        </Field>
        <Field label="Password" htmlFor="new-user-password" hint="At least 8 characters. Share it through a secure channel.">
          <Input id="new-user-password" type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete="new-password" required />
        </Field>
        <div className="space-y-1.5">
          <p className="text-sm font-medium">Roles</p>
          <RolePicker roles={roles} selected={selected} onChange={setSelected} />
          <p className="text-xs text-muted-foreground">Accounts without a role can sign in but have no permissions.</p>
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {busy ? 'Creating…' : 'Create account'}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function EditUserDialog({ user, roles, onClose, onSaved }: {  user: AuthUser
  roles: Role[]
  onClose: () => void
  onSaved: (user: AuthUser) => void
}) {
  const [name, setName] = useState(user.name)
  const [selected, setSelected] = useState(() => roleIdsFor(user, roles))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (name.trim().length < 2) {
      setError('Enter a name of at least 2 characters.')
      return
    }
    setBusy(true)
    setError('')
    try {
      onSaved(await authApi.updateUser(user.id, { name: name.trim(), roles: selected }))
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onOpenChange={next => !next && !busy && onClose()} title={`Edit ${user.name}`} description="Change the account name or assigned roles.">
      <form onSubmit={submit} className="space-y-4">
        <FormError message={error} />
        <Field label="Name" htmlFor="edit-user-name">
          <Input id="edit-user-name" value={name} onChange={event => setName(event.target.value)} required />
        </Field>
        <div className="space-y-1.5">
          <p className="text-sm font-medium">Roles</p>
          <RolePicker roles={roles} selected={selected} onChange={setSelected} />
          <p className="text-xs text-muted-foreground">Roles carry the permissions this account receives.</p>
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {busy ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function ResetPasswordDialog({ user, onClose, onDone }: {
  user: AuthUser
  onClose: () => void
  onDone: () => void
}) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (password.length < 8) {
      setError('Use a password of at least 8 characters.')
      return
    }
    if (password !== confirm) {
      setError('The passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await authApi.resetUserPassword(user.id, password)
      onDone()
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
      title={`Reset password for ${user.name}`}
      description="Set a new password and share it with the account owner."
    >
      <form onSubmit={submit} className="space-y-4">
        <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-500">
          Every signed-in session and API token for this account stops working immediately.
        </p>
        <FormError message={error} />
        <Field label="New password" htmlFor="reset-user-password" hint="At least 8 characters.">
          <Input
            id="reset-user-password"
            type="password"
            value={password}
            onChange={event => setPassword(event.target.value)}
            autoComplete="new-password"
            autoFocus
            required
          />
        </Field>
        <Field label="Confirm new password" htmlFor="reset-user-confirm">
          <Input
            id="reset-user-confirm"
            type="password"
            value={confirm}
            onChange={event => setConfirm(event.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="destructive" disabled={busy}>
            {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {busy ? 'Resetting…' : 'Reset password'}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

export function UsersPeople({ currentUserId }: { currentUserId: string }) {
  const { refresh } = useAuth()
  const [users, setUsers] = useState<AuthUser[] | null>(null)
  const [roles, setRoles] = useState<Role[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<AuthUser | null>(null)
  const [resetting, setResetting] = useState<AuthUser | null>(null)
  const [pendingId, setPendingId] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    void Promise.all([authApi.listUsers(controller.signal), authApi.listRoles(controller.signal)])
      .then(([people, roleList]) => {
        if (!active) return
        setUsers(people)
        setRoles(roleList)
      })
      .catch(cause => {
        if (active && !controller.signal.aborted) setError(errorMessage(cause))
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [attempt])

  function retry() {
    setError('')
    setAttempt(value => value + 1)
  }

  function replaceUser(updated: AuthUser) {
    setUsers(list => list?.map(item => (item.id === updated.id ? updated : item)) ?? null)
    if (updated.id === currentUserId) void refresh().catch(() => undefined)
  }

  async function toggleActive(person: AuthUser) {
    setPendingId(person.id)
    setError('')
    setNotice('')
    try {
      replaceUser(await authApi.updateUser(person.id, { active: !person.active }))
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setPendingId('')
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          {users ? `${users.length} ${users.length === 1 ? 'account' : 'accounts'}` : 'Loading accounts…'}
        </p>
        <Button size="sm" onClick={() => setCreating(true)} disabled={roles.length === 0}>
          <UserPlus aria-hidden="true" />
          Create account
        </Button>
      </div>

      {notice && (
        <p role="status" className="flex items-center gap-2 rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-3 text-sm text-emerald-500">
          <Check className="size-4" aria-hidden="true" />
          {notice}
        </p>
      )}
      {error && <LoadError message={error} onRetry={retry} />}

      {!users && !error && <LoadingRows label="Loading accounts…" />}
      {users && users.length === 0 && <EmptyState title="No accounts yet" description="Create an account to give someone access to this server." />}

      {users && users.length > 0 && (
        <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
          {users.map(person => (
            <li key={person.id} className="flex flex-wrap items-center gap-3 bg-card px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="truncate font-medium">{person.name}</p>
                  {person.id === currentUserId && <Badge variant="outline">You</Badge>}
                  {!person.active && <Badge variant="destructive">Disabled</Badge>}
                </div>
                <p className="truncate text-xs text-muted-foreground">
                  {person.roles.length ? person.roles.join(', ') : 'No role'} ·{' '}
                  {person.lastLoginAt ? `last signed in ${formatAge(person.lastLoginAt)}` : 'never signed in'}
                </p>
              </div>
              <div className="flex items-center gap-1.5">
                <Button variant="outline" size="sm" onClick={() => setEditing(person)} disabled={roles.length === 0}>
                  Edit
                </Button>
                {person.id !== currentUserId && (
                  <>
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={pendingId === person.id}
                      onClick={() => void toggleActive(person)}
                    >
                      {pendingId === person.id && <LoaderCircle className="animate-spin" aria-hidden="true" />}
                      {person.active ? 'Disable' : 'Enable'}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setResetting(person)}>
                      Reset password
                    </Button>
                    <Confirm
                      trigger={
                        <Button variant="ghost" size="sm" className="text-destructive">
                          Delete
                        </Button>
                      }
                      title="Delete account"
                      description={`Delete ${person.name}? Their sessions and API tokens stop working immediately. This cannot be undone.`}
                      confirmLabel="Delete account"
                      onConfirm={async () => {
                        await authApi.deleteUser(person.id)
                        setUsers(list => list?.filter(item => item.id !== person.id) ?? null)
                        setNotice('Account deleted.')
                      }}
                    />
                  </>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      <CreateUserDialog
        open={creating}
        onOpenChange={setCreating}
        roles={roles}
        onCreated={created => {
          setUsers(list => (list ? [...list, created] : [created]))
          setCreating(false)
          setNotice('Account created.')
        }}
      />
      {editing && (
        <EditUserDialog
          key={editing.id}
          user={editing}
          roles={roles}
          onClose={() => setEditing(null)}
          onSaved={saved => {
            replaceUser(saved)
            setEditing(null)
            setNotice('Account updated.')
          }}
        />
      )}
      {resetting && (
        <ResetPasswordDialog
          key={resetting.id}
          user={resetting}
          onClose={() => setResetting(null)}
          onDone={() => {
            setNotice(`Password reset for ${resetting.name}. Their previous sessions and tokens no longer work.`)
            setResetting(null)
          }}
        />
      )}
    </div>
  )
}
