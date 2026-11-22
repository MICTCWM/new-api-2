/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useRouter } from '@tanstack/react-router'
import {
  ArrowLeft,
  Loader2,
  RefreshCw,
  Trash2,
  Undo2,
  Upload,
} from 'lucide-react'
import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  deleteCommandGoAccounts,
  getChannel,
  getCommandGoAccounts,
  recoverCommandGoAccounts,
  refreshAllCommandGoAccounts,
  refreshCommandGoAccount,
} from '../../api'
import { CHANNEL_TYPE_COMMAND_GO } from '../../constants'
import {
  commandGoAccountsQueryKey,
  getApiErrorMessage,
  getChannelTypeLabel,
  channelsQueryKeys,
} from '../../lib'
import type { CommandGoDeleteAccountsParams } from '../../types'
import { CommandGoAccountCard } from './command-go-account-card'
import { CommandGoImportDialog } from './command-go-import-dialog'

type ConfirmTarget =
  | { kind: 'delete-selected' }
  | { kind: 'delete-status'; status: 'error' | 'no_quota' }
  | null

export function CommandGoPoolPage(props: { channelId: number }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const router = useRouter()
  const queryClient = useQueryClient()
  const channelId = props.channelId
  const queryKey = commandGoAccountsQueryKey(channelId)
  const currentUser = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )

  const [importOpen, setImportOpen] = useState(false)
  const [selectedIds, setSelectedIds] = useState<number[]>([])
  const [confirmTarget, setConfirmTarget] = useState<ConfirmTarget>(null)

  const channelQuery = useQuery({
    queryKey: channelsQueryKeys.detail(channelId),
    queryFn: () => getChannel(channelId),
  })
  const accountsQuery = useQuery({
    queryKey,
    queryFn: async () => {
      const response = await getCommandGoAccounts(channelId)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Failed to load accounts'))
      }
      return response
    },
  })

  const invalidateAccounts = () => queryClient.invalidateQueries({ queryKey })

  const refreshAllMutation = useMutation({
    mutationFn: async () => {
      const response = await refreshAllCommandGoAccounts(channelId)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Failed to refresh accounts'))
      }
      return response.data
    },
    onSuccess: (data) => {
      if (data.failed.length > 0) {
        const first = data.failed[0]
        const detail = `${first.name}: ${first.error}`
        if (data.refreshed === 0) {
          toast.error(t('Refresh failed: {{error}}', { error: detail }))
        } else {
          toast.warning(
            t(
              '{{success}} account(s) refreshed, {{failed}} failed. First: {{detail}}',
              {
                success: data.refreshed,
                failed: data.failed.length,
                detail,
              }
            )
          )
        }
      } else {
        toast.success(
          t('{{success}} account(s) refreshed, {{failed}} failed', {
            success: data.refreshed,
            failed: 0,
          })
        )
      }
      void invalidateAccounts()
    },
    onError: (error: unknown) => {
      toast.error(getApiErrorMessage(error, t('Failed to refresh accounts')))
    },
  })

  const refreshAccountMutation = useMutation({
    mutationFn: async (accountId: number) => {
      const response = await refreshCommandGoAccount(channelId, accountId)
      if (!response.success) {
        throw new Error(response.message || t('Failed to refresh account'))
      }
      return response.data
    },
    onSuccess: () => {
      toast.success(t('Account refreshed'))
      void invalidateAccounts()
    },
    onError: (error: unknown) => {
      toast.error(getApiErrorMessage(error, t('Failed to refresh account')))
    },
  })

  const recoverMutation = useMutation({
    mutationFn: async (ids: number[]) => {
      const response = await recoverCommandGoAccounts(channelId, ids)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Failed to recover accounts'))
      }
      return response.data
    },
    onSuccess: (data) => {
      toast.success(
        t('Recovered {{count}} account(s)', { count: data.updated })
      )
      setSelectedIds([])
      void invalidateAccounts()
    },
    onError: (error: unknown) => {
      toast.error(getApiErrorMessage(error, t('Failed to recover accounts')))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: async (params: CommandGoDeleteAccountsParams) => {
      const response = await deleteCommandGoAccounts(channelId, params)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Failed to delete accounts'))
      }
      return response.data
    },
    onSuccess: (data) => {
      toast.success(t('Deleted {{count}} account(s)', { count: data.deleted }))
      setSelectedIds([])
      void invalidateAccounts()
    },
    onError: (error: unknown) => {
      toast.error(getApiErrorMessage(error, t('Failed to delete accounts')))
    },
  })

  const accounts = accountsQuery.data?.data?.accounts ?? []
  const summary = accountsQuery.data?.data?.summary
  const accountIds = new Set(accounts.map((account) => account.id))
  const selected = selectedIds.filter((id) => accountIds.has(id))
  const refreshBusy =
    refreshAllMutation.isPending || refreshAccountMutation.isPending
  const channel = channelQuery.data?.data
  const channelName = channel?.name || t('Command Go')
  const channelType = channel?.type ?? CHANNEL_TYPE_COMMAND_GO
  const allSelected = accounts.length > 0 && selected.length === accounts.length

  // Drop stale selections after the list refreshes.
  useEffect(() => {
    setSelectedIds((current) => {
      const next = current.filter((id) => accountIds.has(id))
      return next.length === current.length ? current : next
    })
    // accountIds is derived from accounts; re-run only when that list changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountsQuery.data])

  const toggleSelected = (id: number, checked: boolean) => {
    setSelectedIds((current) => {
      if (checked) {
        return current.includes(id) ? current : [...current, id]
      }
      return current.filter((item) => item !== id)
    })
  }

  const toggleSelectAll = (checked: boolean) => {
    setSelectedIds(checked ? accounts.map((account) => account.id) : [])
  }

  const handleConfirmDelete = () => {
    if (!confirmTarget) return
    if (confirmTarget.kind === 'delete-selected') {
      deleteMutation.mutate({ ids: selected })
    } else {
      deleteMutation.mutate({ status: confirmTarget.status })
    }
    setConfirmTarget(null)
  }

  const confirmCopy = (() => {
    if (!confirmTarget) {
      return { title: '', desc: '' }
    }
    if (confirmTarget.kind === 'delete-selected') {
      return {
        title: t('Delete {{count}} selected account(s)?', {
          count: selected.length,
        }),
        desc: t('This action cannot be undone.'),
      }
    }
    if (confirmTarget.status === 'error') {
      return {
        title: t('Delete all error accounts?'),
        desc: t(
          'Every account with an error status in this pool will be permanently removed. This action cannot be undone.'
        ),
      }
    }
    return {
      title: t('Delete all no-quota accounts?'),
      desc: t(
        'Every account without available quota in this pool will be permanently removed. This action cannot be undone.'
      ),
    }
  })()

  const handleBack = () => {
    if (router.history.canGoBack()) {
      router.history.back()
      return
    }
    navigate({ to: '/channels' })
  }

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Breadcrumb>
          <Button
            variant='ghost'
            size='sm'
            className='-ml-2'
            onClick={handleBack}
          >
            <ArrowLeft className='size-4' />
            {t('Back')}
          </Button>
        </SectionPageLayout.Breadcrumb>
        <SectionPageLayout.Title>
          <span className='flex min-w-0 items-center gap-2'>
            <span className='truncate'>{channelName}</span>
            <Badge variant='outline' className='shrink-0'>
              {t(getChannelTypeLabel(channelType))}
            </Badge>
          </span>
        </SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button
            variant='outline'
            size='sm'
            onClick={() => setImportOpen(true)}
            disabled={!canOperate}
          >
            <Upload className='size-4' />
            {t('Import Accounts')}
          </Button>
          <Button
            size='sm'
            onClick={() => refreshAllMutation.mutate()}
            disabled={!canOperate || refreshBusy || accounts.length === 0}
          >
            {refreshAllMutation.isPending ? (
              <Loader2 className='size-4 animate-spin' />
            ) : (
              <RefreshCw className='size-4' />
            )}
            {t('Update All Balances')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-3'>
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div className='flex flex-wrap items-center gap-2'>
                <StatusBadge
                  label={`${t('Total')}: ${summary?.total ?? accounts.length}`}
                  variant='neutral'
                  copyable={false}
                />
                <StatusBadge
                  label={`${t('Active')}: ${summary?.active ?? 0}`}
                  variant='success'
                  copyable={false}
                />
                <StatusBadge
                  label={`${t('Error')}: ${summary?.error ?? 0}`}
                  variant='danger'
                  copyable={false}
                />
                <StatusBadge
                  label={`${t('No Quota')}: ${summary?.no_quota ?? 0}`}
                  variant='neutral'
                  copyable={false}
                />
              </div>
              <div className='flex flex-wrap items-center gap-2'>
                <Button
                  variant='outline'
                  size='sm'
                  className='text-destructive hover:text-destructive'
                  onClick={() =>
                    setConfirmTarget({ kind: 'delete-status', status: 'error' })
                  }
                  disabled={
                    !canOperate || deleteMutation.isPending || !summary?.error
                  }
                >
                  <Trash2 className='size-4' />
                  {t('Delete All Error Accounts')}
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  className='text-destructive hover:text-destructive'
                  onClick={() =>
                    setConfirmTarget({
                      kind: 'delete-status',
                      status: 'no_quota',
                    })
                  }
                  disabled={
                    !canOperate ||
                    deleteMutation.isPending ||
                    !summary?.no_quota
                  }
                >
                  <Trash2 className='size-4' />
                  {t('Delete All No-Quota Accounts')}
                </Button>
              </div>
            </div>

            <div className='flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2'>
              <label className='flex items-center gap-2 text-sm'>
                <Checkbox
                  checked={allSelected}
                  indeterminate={selected.length > 0 && !allSelected}
                  onCheckedChange={(checked) => toggleSelectAll(checked)}
                  aria-label={t('Select All')}
                />
                <span>
                  {t('Selected {{count}}', { count: selected.length })}
                </span>
              </label>
              <div className='flex items-center gap-2'>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => recoverMutation.mutate(selected)}
                  disabled={
                    !canOperate ||
                    selected.length === 0 ||
                    recoverMutation.isPending
                  }
                >
                  {recoverMutation.isPending ? (
                    <Loader2 className='size-4 animate-spin' />
                  ) : (
                    <Undo2 className='size-4' />
                  )}
                  {t('Recover Selected')}
                </Button>
                <Button
                  variant='destructive'
                  size='sm'
                  onClick={() => setConfirmTarget({ kind: 'delete-selected' })}
                  disabled={
                    !canOperate ||
                    selected.length === 0 ||
                    deleteMutation.isPending
                  }
                >
                  <Trash2 className='size-4' />
                  {t('Delete Selected')}
                </Button>
              </div>
            </div>

            <div className='min-h-0 flex-1 overflow-auto'>
              {accountsQuery.isPending && (
                <div className='text-muted-foreground flex items-center gap-2 py-8 text-sm'>
                  <Loader2 className='size-4 animate-spin' />
                  {t('Loading')}
                </div>
              )}
              {accountsQuery.isError && (
                <Alert variant='destructive'>
                  <AlertDescription>
                    {getApiErrorMessage(
                      accountsQuery.error,
                      t('Failed to load accounts')
                    )}
                  </AlertDescription>
                </Alert>
              )}
              {!accountsQuery.isPending &&
                !accountsQuery.isError &&
                accounts.length === 0 && (
                  <div className='text-muted-foreground py-10 text-center text-sm'>
                    {t(
                      'No accounts yet. Import user_* keys to create the pool.'
                    )}
                  </div>
                )}
              {accounts.length > 0 && (
                <div className='grid gap-3 lg:grid-cols-2'>
                  {accounts.map((account) => (
                    <CommandGoAccountCard
                      key={account.id}
                      account={account}
                      selected={selected.includes(account.id)}
                      refreshing={
                        refreshAccountMutation.isPending &&
                        refreshAccountMutation.variables === account.id
                      }
                      refreshDisabled={!canOperate || refreshBusy}
                      onSelectedChange={(checked) =>
                        toggleSelected(account.id, checked)
                      }
                      onRefresh={() =>
                        refreshAccountMutation.mutate(account.id)
                      }
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <CommandGoImportDialog
        channelId={channelId}
        open={importOpen}
        onOpenChange={setImportOpen}
      />

      <ConfirmDialog
        open={confirmTarget !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmTarget(null)
        }}
        title={confirmCopy.title}
        desc={confirmCopy.desc}
        confirmText={t('Delete')}
        destructive
        isLoading={deleteMutation.isPending}
        handleConfirm={handleConfirmDelete}
      />
    </>
  )
}
