import { useEffect, useState, type FormEvent } from 'react'
import { Check, LoaderCircle, Plus } from 'lucide-react'
import { Field, FormError } from '@/components/auth-gate'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Confirm, EmptyState, LoadError, LoadingRows, Modal } from '@/components/users-shared'
import { errorMessage } from '@/lib/api'
import { authApi, permissionGroup, permissionLabel, type Role } from '@/lib/auth-api'

function permissionGroups(catalog: string[], selected: string[]) {
  const keys = [...catalog, ...selected.filter(key => !catalog.includes(key))]
  const groups = new Map<string, string[]>()
  for (const key of keys) {
    const group = permissionGroup(key)
    groups.set(group, [...(groups.get(group) ?? []), key])
  }
  return [...groups.entries()]
}

function PermissionPicker({ catalog, selected, onChange }: {
  catalog: string[]
  selected: string[]
  onChange: (next: string[]) => void
}) {
  function toggle(key: string) {
    onChange(selected.includes(key) ? selected.filter(item => item !== key) : [...selected, key])
  }

  return (
    <div className="max-h-72 space-y-4 overflow-y-auto rounded-lg border border-border p-3">
      {permissionGroups(catalog, selected).map(([group, keys]) => (
        <fieldset key={group}>
          <legend className="mb-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">{group}</legend>
          <div className="space-y-1">
            {keys.map(key => (
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
          </div>
        </fieldset>
      ))}
      {catalog.length === 0 && <p className="text-sm text-muted-foreground">The permission catalog is empty.</p>}
    </div>
  )
}

function RoleDialog({ role, catalog, onClose, onSaved }: {
  role: Role | null
  catalog: string[]
  onClose: () => void
  onSaved: (role: Role, created: boolean) => void
}) {
  const [name, setName] = useState(role?.name ?? '')
  const [permissions, setPermissions] = useState<string[]>(role?.permissions ?? [])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (name.trim().length < 2) {
      setError('Enter a role name of at least 2 characters.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const body = { name: name.trim(), permissions }
      onSaved(role ? await authApi.updateRole(role.id, body) : await authApi.createRole(body), !role)
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
      title={role ? `Edit ${role.name}` : 'Create role'}
      description="Roles bundle permissions that can be assigned to accounts."
      className="max-w-lg"
    >
      <form onSubmit={submit} className="space-y-4">
        <FormError message={error} />
        <Field label="Name" htmlFor="role-name">
          <Input id="role-name" value={name} onChange={event => setName(event.target.value)} required />
        </Field>
        <div className="space-y-1.5">
          <p className="text-sm font-medium">Permissions</p>
          <PermissionPicker catalog={catalog} selected={permissions} onChange={setPermissions} />
          <p className="text-xs text-muted-foreground">{permissions.length} selected</p>
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {busy ? 'Saving…' : role ? 'Save role' : 'Create role'}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

export function UsersRoles() {
  const [roles, setRoles] = useState<Role[] | null>(null)
  const [catalog, setCatalog] = useState<string[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [editing, setEditing] = useState<Role | null>(null)
  const [creating, setCreating] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    void Promise.all([authApi.listRoles(controller.signal), authApi.listPermissions(controller.signal)])
      .then(([roleList, permissions]) => {
        if (!active) return
        setRoles(roleList)
        setCatalog(permissions)
      })
      .catch(cause => {
        if (active && !controller.signal.aborted) setError(errorMessage(cause))
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [attempt])

  function replaceRole(updated: Role, created: boolean) {
    setRoles(list => (created ? [...(list ?? []), updated] : list?.map(role => (role.id === updated.id ? updated : role)) ?? null))
    setEditing(null)
    setCreating(false)
    setNotice(created ? `Role ${updated.name} created.` : `Role ${updated.name} updated.`)
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">Built-in roles are managed by Constellarr. Create custom roles for other access levels.</p>
        <Button
          size="sm"
          onClick={() => {
            setNotice('')
            setCreating(true)
          }}
          disabled={catalog.length === 0}
        >
          <Plus aria-hidden="true" />
          Create role
        </Button>
      </div>

      {notice && (
        <p role="status" className="flex items-center gap-2 rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-3 text-sm text-emerald-500">
          <Check className="size-4" aria-hidden="true" />
          {notice}
        </p>
      )}

      {error && (
        <LoadError
          message={error}
          onRetry={() => {
            setError('')
            setNotice('')
            setAttempt(value => value + 1)
          }}
        />
      )}
      {!roles && !error && <LoadingRows label="Loading roles…" />}
      {roles && roles.length === 0 && <EmptyState title="No roles" description="Create a role to describe an access level." />}

      {roles && roles.length > 0 && (
        <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
          {roles.map(role => (
            <li key={role.id} className="flex flex-wrap items-center gap-3 bg-card px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="truncate font-medium">{role.name}</p>
                  {role.builtin && <Badge variant="secondary">Built-in</Badge>}
                </div>
                <p className="truncate text-xs text-muted-foreground">
                  {role.permissions.length} {role.permissions.length === 1 ? 'permission' : 'permissions'}
                </p>
              </div>
              {!role.builtin && (
                <div className="flex items-center gap-1.5">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      setNotice('')
                      setEditing(role)
                    }}
                    disabled={catalog.length === 0}
                  >
                    Edit
                  </Button>
                  <Confirm
                    trigger={
                      <Button variant="ghost" size="sm" className="text-destructive">
                        Delete
                      </Button>
                    }
                    title="Delete role"
                    description={`Delete ${role.name}? Accounts using it must be reassigned first.`}
                    confirmLabel="Delete role"
                    onConfirm={async () => {
                      await authApi.deleteRole(role.id)
                      setRoles(list => list?.filter(item => item.id !== role.id) ?? null)
                      setNotice(`Role ${role.name} deleted.`)
                    }}
                  />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      {creating && <RoleDialog role={null} catalog={catalog} onClose={() => setCreating(false)} onSaved={replaceRole} />}
      {editing && (
        <RoleDialog key={editing.id} role={editing} catalog={catalog} onClose={() => setEditing(null)} onSaved={replaceRole} />
      )}
    </div>
  )
}
