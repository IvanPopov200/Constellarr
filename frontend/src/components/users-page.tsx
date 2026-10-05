import type { ReactNode } from 'react'
import { Tabs } from 'radix-ui'
import { PageHeading } from '@/components/app-shell'
import { UsersAudit } from '@/components/users-audit'
import { UsersPeople } from '@/components/users-people'
import { UsersRoles } from '@/components/users-roles'
import { UsersSecurity } from '@/components/users-security'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { cn } from 'cn'

function TabTrigger({ value, children }: { value: string; children: ReactNode }) {
  return (
    <Tabs.Trigger
      value={value}
      className={cn(
        '-mb-px border-b-2 border-transparent px-3 py-2 text-sm font-medium text-muted-foreground transition-colors',
        'hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-0',
        'data-[state=active]:border-primary data-[state=active]:text-foreground',
      )}
    >
      {children}
    </Tabs.Trigger>
  )
}

export function UsersPage() {
  const { can, user } = useAuth()
  const manage = can(accessPermissions.usersManage)
  const defaultTab = manage ? 'users' : 'account'

  return (
    <div className="space-y-6">
      <PageHeading
        title="Users & Access"
        description="Accounts, roles, personal API tokens, and the administrative audit trail."
      />
      {!manage && (
        <p className="rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
          You can manage your own account here. Viewing accounts, roles, and the audit log requires the user management
          permission.
        </p>
      )}
      <Tabs.Root key={defaultTab} defaultValue={defaultTab}>
        <Tabs.List aria-label="Access sections" className="flex flex-wrap items-center gap-1 border-b border-border">
          {manage && <TabTrigger value="users">Users</TabTrigger>}
          {manage && <TabTrigger value="roles">Roles</TabTrigger>}
          {manage && <TabTrigger value="audit">Audit log</TabTrigger>}
          <TabTrigger value="account">My account</TabTrigger>
        </Tabs.List>
        {manage && (
          <Tabs.Content value="users" className="mt-6">
            <UsersPeople currentUserId={user?.id ?? ''} />
          </Tabs.Content>
        )}
        {manage && (
          <Tabs.Content value="roles" className="mt-6">
            <UsersRoles />
          </Tabs.Content>
        )}
        {manage && (
          <Tabs.Content value="audit" className="mt-6">
            <UsersAudit />
          </Tabs.Content>
        )}
        <Tabs.Content value="account" className="mt-6">
          <UsersSecurity />
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
