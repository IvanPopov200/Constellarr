import { ApiError } from '@/lib/api'

// Client for the /api/v1 auth, user, role, permission, token, and audit endpoints.
const basePath = '/api/v1'

export class UnauthorizedError extends ApiError {}
export class ForbiddenError extends ApiError {}
export class ContractError extends ApiError {}

export type AuthUser = {
  id: string
  name: string
  active: boolean
  roles: string[]
  permissions: string[]
  createdAt: string
  lastLoginAt: string | null
}

export type Session = { user: AuthUser; permissions: string[] }

export type AuthStatusResponse = {
  setupRequired: boolean
  authenticated: boolean
  session: Session | null
}

export type LoginInput = { name: string; password: string }
export type SetupInput = { name: string; password: string }
export type PasswordChangeInput = { currentPassword: string; newPassword: string }

export type UserCreateInput = { name: string; password: string; active: boolean; roles: string[] }
export type UserUpdateInput = { name?: string; active?: boolean; roles?: string[] }

export type Role = {
  id: string
  name: string
  builtin: boolean
  permissions: string[]
}

export type RoleInput = { name: string; permissions: string[] }

export type ApiToken = {
  id: string
  name: string
  permissions: string[]
  createdAt: string
  expiresAt: string | null
  lastUsedAt: string | null
}

// A null permission list grants the token every permission the account holds.
export type TokenInput = { name: string; permissions: string[] | null; expiresAt: string | null }
export type TokenSecret = { token: ApiToken; secret: string }

export type AuditEntry = {
  at: string
  actorId: string
  actorName: string
  action: string
  target: string
  outcome: string
}

// Keys used to gate this UI; the server permission catalog remains the source of truth.
export const accessPermissions = {
  libraryRead: 'library.read',
  libraryWrite: 'library.write',
  downloadsRead: 'downloads.read',
  downloadsWrite: 'downloads.write',
  settingsRead: 'settings.read',
  settingsWrite: 'settings.write',
  usersManage: 'users.manage',
} as const

type HttpOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  body?: unknown
  signal?: AbortSignal
}

function requestSignal(signal?: AbortSignal, timeoutMs = 15000) {
  if (typeof AbortSignal.any !== 'function') return signal
  const timeout = AbortSignal.timeout(timeoutMs)
  return signal ? AbortSignal.any([signal, timeout]) : timeout
}

async function http(path: string, options: HttpOptions = {}): Promise<unknown> {
  const { method = 'GET', body, signal } = options
  let response: Response

  try {
    response = await fetch(`${basePath}${path}`, {
      method,
      cache: 'no-store',
      credentials: 'same-origin',
      signal: requestSignal(signal),
      headers: {
        Accept: 'application/json',
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new ApiError('The backend could not be reached. Confirm the server is running, then retry.')
  }

  if (response.status === 401) throw new UnauthorizedError('Your session expired. Sign in again.')
  if (response.status === 403) {
    throw new ForbiddenError((await errorDetail(response)) ?? 'You do not have permission to do that.')
  }
  if (!response.ok) {
    throw new ApiError((await errorDetail(response)) ?? `The server returned HTTP ${response.status}.`)
  }
  if (response.status === 204) return null

  const text = await response.text()
  if (!text) return null
  try {
    return JSON.parse(text) as unknown
  } catch {
    throw new ContractError('The server returned an unreadable response.')
  }
}

async function errorDetail(response: Response) {
  const detail = (await response.json().catch(() => null)) as { error?: unknown } | null
  const message = detail?.error
  return typeof message === 'string' && message.trim() ? message : null
}

function record(value: unknown, what: string) {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new ContractError(`The server returned an unreadable ${what} response.`)
  }
  return value as Record<string, unknown>
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function text(value: unknown, field: string) {
  if (typeof value === 'string') return value
  throw new ContractError(`The server response was missing ${field}.`)
}

function optionalText(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function nullableText(value: unknown) {
  return typeof value === 'string' && value ? value : null
}

function flag(value: unknown) {
  return typeof value === 'boolean' ? value : false
}

function stringList(value: unknown, field: string) {
  if (value === undefined || value === null) return []
  if (!Array.isArray(value)) throw new ContractError(`The server response had an invalid ${field}.`)
  return value.filter((item): item is string => typeof item === 'string')
}

// List endpoints answer with a bare array; the permission catalog is wrapped in an object.
function list(value: unknown, wrapper?: string) {
  if (Array.isArray(value)) return value
  if (wrapper && isRecord(value) && Array.isArray(value[wrapper])) return value[wrapper] as unknown[]
  throw new ContractError('The server returned an unexpected list response.')
}

function decodeUser(value: unknown): AuthUser {
  const raw = record(value, 'user')
  return {
    id: text(raw.id, 'id'),
    name: text(raw.name, 'name'),
    active: flag(raw.active),
    roles: stringList(raw.roles, 'roles'),
    permissions: stringList(raw.permissions, 'permissions'),
    createdAt: optionalText(raw.createdAt),
    lastLoginAt: nullableText(raw.lastLoginAt),
  }
}

function sessionFromUser(value: unknown): Session {
  const user = decodeUser(value)
  return { user, permissions: user.permissions }
}

export function decodeStatus(value: unknown): AuthStatusResponse {
  const raw = record(value, 'status')
  const setupRequired = flag(raw.setupRequired)
  const hasUser = isRecord(raw.user)
  return {
    setupRequired,
    authenticated: flag(raw.authenticated) || hasUser,
    session: hasUser ? sessionFromUser(raw.user) : null,
  }
}

export function decodeSession(value: unknown): Session {
  const raw = record(value, 'session')
  return sessionFromUser(isRecord(raw.user) ? raw.user : raw)
}

export function decodeUsers(value: unknown): AuthUser[] {
  return list(value).map(decodeUser)
}

function decodeRole(value: unknown): Role {
  const raw = record(value, 'role')
  return {
    id: text(raw.id, 'id'),
    name: text(raw.name, 'name'),
    builtin: flag(raw.builtin),
    permissions: stringList(raw.permissions, 'permissions'),
  }
}

export function decodeRoles(value: unknown): Role[] {
  return list(value).map(decodeRole)
}

export function decodePermissions(value: unknown): string[] {
  return list(value, 'permissions').filter((item): item is string => typeof item === 'string')
}

function decodeToken(value: unknown): ApiToken {
  const raw = record(value, 'token')
  return {
    id: text(raw.id, 'id'),
    name: text(raw.name, 'name'),
    permissions: stringList(raw.permissions, 'permissions'),
    createdAt: optionalText(raw.createdAt),
    expiresAt: nullableText(raw.expiresAt),
    lastUsedAt: nullableText(raw.lastUsedAt),
  }
}

export function decodeTokens(value: unknown): ApiToken[] {
  return list(value).map(decodeToken)
}

export function decodeTokenSecret(value: unknown): TokenSecret {
  const raw = record(value, 'token')
  return { token: decodeToken(raw), secret: text(raw.token ?? raw.secret, 'token') }
}

function decodeAuditEntry(value: unknown): AuditEntry {
  const raw = record(value, 'audit entry')
  return {
    at: text(raw.at, 'at'),
    actorId: optionalText(raw.actorId),
    actorName: optionalText(raw.actorName),
    action: text(raw.action, 'action'),
    target: optionalText(raw.target),
    outcome: optionalText(raw.outcome),
  }
}

export function decodeAudit(value: unknown): AuditEntry[] {
  return list(value).map(decodeAuditEntry)
}

// Permission catalog is shared; clearing it on sign-out keeps privileged data off screen.
let permissionCatalog: string[] | null = null

export function clearAuthCache() {
  permissionCatalog = null
}

// Mirrors server-side grant checks so the UI hides what the API would reject.
export function hasPermission(granted: string[], permission: string | string[], mode: 'all' | 'any' = 'all') {
  const wanted = Array.isArray(permission) ? permission : [permission]
  if (wanted.length === 0) return true
  const matched = wanted.filter(item => granted.includes(item)).length
  return mode === 'any' ? matched > 0 : matched === wanted.length
}

function titleCase(value: string) {
  return value ? value[0].toUpperCase() + value.slice(1) : value
}

// Permission keys are the server contract; display text is derived so unknown keys still render.
export function permissionLabel(key: string) {
  const [group, ...rest] = key.split('.')
  const action = rest.map(titleCase).join(' ')
  return action ? `${titleCase(group)}: ${action}` : titleCase(key)
}

export function permissionGroup(key: string) {
  const [group] = key.split('.')
  return titleCase(group ?? key)
}

export const authApi = {
  status: (signal?: AbortSignal) => http('/auth/status', { signal }).then(decodeStatus),
  setup: (body: SetupInput) => http('/auth/setup', { method: 'POST', body }).then(decodeSession),
  login: (body: LoginInput) => http('/auth/login', { method: 'POST', body }).then(decodeSession),
  logout: () => http('/auth/logout', { method: 'POST' }),
  me: (signal?: AbortSignal) => http('/auth/me', { signal }).then(decodeSession),
  changePassword: (body: PasswordChangeInput) => http('/auth/password', { method: 'PUT', body }),

  listUsers: (signal?: AbortSignal) => http('/users', { signal }).then(decodeUsers),
  createUser: (body: UserCreateInput) => http('/users', { method: 'POST', body }).then(decodeUser),
  updateUser: (id: string, body: UserUpdateInput) =>
    http(`/users/${encodeURIComponent(id)}`, { method: 'PUT', body }).then(decodeUser),
  deleteUser: (id: string) => http(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  resetUserPassword: (id: string, password: string) =>
    http(`/users/${encodeURIComponent(id)}/password`, { method: 'PUT', body: { password } }),

  listRoles: (signal?: AbortSignal) => http('/roles', { signal }).then(decodeRoles),
  createRole: (body: RoleInput) => http('/roles', { method: 'POST', body }).then(decodeRole),
  updateRole: (id: string, body: Partial<RoleInput>) =>
    http(`/roles/${encodeURIComponent(id)}`, { method: 'PUT', body }).then(decodeRole),
  deleteRole: (id: string) => http(`/roles/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  async listPermissions(signal?: AbortSignal) {
    if (permissionCatalog) return permissionCatalog
    const permissions = await http('/permissions', { signal }).then(decodePermissions)
    permissionCatalog = permissions
    return permissions
  },

  listTokens: (signal?: AbortSignal) => http('/auth/tokens', { signal }).then(decodeTokens),
  createToken: (body: TokenInput) =>
    http('/auth/tokens', { method: 'POST', body }).then(decodeTokenSecret),
  revokeToken: (id: string) => http(`/auth/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  listAudit: (limit = 200, signal?: AbortSignal) =>
    http(`/audit?limit=${limit}`, { signal }).then(decodeAudit),
}
