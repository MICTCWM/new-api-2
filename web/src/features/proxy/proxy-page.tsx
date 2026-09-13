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
import {
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyTitle,
} from '@/components/ui/empty'
import {
  clearProxySubscription,
  createProxyRule,
  deleteProxyRule,
  getProxyStatus,
  refreshProxySubscription,
  saveProxySubscription,
  updateProxyRule,
} from './api'
import { ProxyNodeList } from './components/proxy-node-list'
import { ProxySubscriptionCard } from './components/proxy-subscription-card'
import { RuleMutateDrawer } from './components/rule-mutate-drawer'
import { formatNodeAddress } from './lib'
import type {
  CreateProxyRuleParams,
  ProxyRule,
} from './types'

/**
 * Admin page for managing the proxy gateway: subscription URL, parsed nodes
 * and routing rules. Action feedback surfaces via toasts; the backend message
 * is shown on business failure.
 */
export function ProxyPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [draftUrl, setDraftUrl] = useState('')
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<ProxyRule | null>(null)
  const [clearOpen, setClearOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<ProxyRule | null>(null)

  const statusQuery = useQuery({
    queryKey: ['proxy-status'],
    queryFn: getProxyStatus,
    retry: false,
    staleTime: 30 * 1000,
  })

  const statusData = statusQuery.data?.success ? statusQuery.data.data : undefined
  const subscriptionConfigured = Boolean(statusData?.subscription_url)
  const nodes = statusData?.nodes ?? []
  const rules = statusData?.rules ?? []

  const invalidateStatus = () =>
    queryClient.invalidateQueries({ queryKey: ['proxy-status'] })

  const saveMutation = useMutation({
    mutationFn: () => saveProxySubscription({ url: draftUrl }),
    onSuccess: (result) => {
      if (result?.success) {
        toast.success(
          t('Pulled {{count}} node(s)', { count: result.data?.node_count ?? 0 })
        )
        setDraftUrl('')
        invalidateStatus()
        return
      }
      toast.error(result?.message || t('Failed to pull subscription'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to pull subscription'))
    },
  })

  const refreshMutation = useMutation({
    mutationFn: () => refreshProxySubscription(),
    onSuccess: (result) => {
      if (result?.success) {
        toast.success(
          t('Pulled {{count}} node(s)', { count: result.data?.node_count ?? 0 })
        )
        invalidateStatus()
        return
      }
      toast.error(result?.message || t('Failed to refresh subscription'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to refresh subscription'))
    },
  })

  const clearMutation = useMutation({
    mutationFn: () => clearProxySubscription(),
    onSuccess: (result) => {
      setClearOpen(false)
      if (result?.success) {
        toast.success(t('Subscription cleared'))
        invalidateStatus()
        return
      }
      toast.error(result?.message || t('Failed to clear subscription'))
    },
    onError: (error: Error) => {
      setClearOpen(false)
      toast.error(error.message || t('Failed to clear subscription'))
    },
  })

  const createRuleMutation = useMutation({
    mutationFn: (params: CreateProxyRuleParams) => createProxyRule(params),
    onSuccess: (result) => {
      if (result?.success) {
        setDrawerOpen(false)
        toast.success(t('Proxy rule created'))
        invalidateStatus()
        return
      }
      toast.error(result?.message || t('Failed to create proxy rule'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to create proxy rule'))
    },
  })

  const updateRuleMutation = useMutation({
    mutationFn: (params: { id: string } & CreateProxyRuleParams) =>
      updateProxyRule(params.id, params),
    onSuccess: (result) => {
      if (result?.success) {
        setDrawerOpen(false)
        toast.success(t('Proxy rule updated'))
        invalidateStatus()
        return
      }
      toast.error(result?.message || t('Failed to update proxy rule'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to update proxy rule'))
    },
  })

  const deleteRuleMutation = useMutation({
    mutationFn: (id: string) => deleteProxyRule(id),
    onSuccess: (result) => {
      setDeleteTarget(null)
      if (result?.success) {
        toast.success(t('Proxy rule deleted'))
        invalidateStatus()
        return
      }
      toast.error(result?.message || t('Failed to delete proxy rule'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to delete proxy rule'))
    },
  })

  return (
    <>
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Proxy')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button size='sm' onClick={() => setDrawerOpen(true)}>
          <Plus className='mr-1 h-3 w-3' />
          {t('New Rule')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        {statusQuery.isError ? (
          <Empty>
            <EmptyTitle>{t('Failed to load proxy status')}</EmptyTitle>
            <EmptyDescription>
              {t('Refresh the list and try again.')}
            </EmptyDescription>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => statusQuery.refetch()}
            >
              <RefreshCw className='mr-1 h-3 w-3' />
              {t('Retry')}
            </Button>
          </Empty>
        ) : (
        <div className='grid gap-4'>
          <ProxySubscriptionCard
            configured={subscriptionConfigured}
            subscriptionUrl={statusData?.subscription_url ?? ''}
            fetchedAt={statusData?.subscription_fetched_at ?? 0}
            draftUrl={draftUrl}
            onDraftUrlChange={setDraftUrl}
            savePending={saveMutation.isPending}
            refreshPending={refreshMutation.isPending}
            clearPending={clearMutation.isPending}
            onSave={() => saveMutation.mutate()}
            onRefresh={() => refreshMutation.mutate()}
            onClear={() => setClearOpen(true)}
          />

          <div className='grid grid-cols-1 gap-4 lg:grid-cols-2'>
            <ProxyNodeList nodes={nodes} loading={statusQuery.isLoading} />
            <RuleTable
              rules={rules}
              loading={statusQuery.isLoading}
              onEdit={(rule) => {
                setEditTarget(rule)
                setDrawerOpen(true)
              }}
              onDelete={setDeleteTarget}
            />
          </div>
        </div>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>

    <RuleMutateDrawer
      open={drawerOpen}
      onOpenChange={(open) => {
        setDrawerOpen(open)
        if (!open) setEditTarget(null)
      }}
      nodes={nodes}
      editing={editTarget}
      isPending={createRuleMutation.isPending || updateRuleMutation.isPending}
      onSubmit={(params) => {
        if (editTarget) {
          updateRuleMutation.mutate({ id: editTarget.id, ...params })
          return
        }
        createRuleMutation.mutate(params)
      }}
    />

    <ConfirmDialog
      open={clearOpen}
      onOpenChange={setClearOpen}
      title={t('Clear subscription')}
      desc={t(
        'This will remove the configured subscription URL, all parsed nodes and routing rules.'
      )}
      destructive
      confirmText={t('Clear')}
      isLoading={clearMutation.isPending}
      handleConfirm={() => clearMutation.mutate()}
    />

    <ConfirmDialog
      open={deleteTarget !== null}
      onOpenChange={(open) => !open && setDeleteTarget(null)}
      title={t('Delete rule')}
      desc={
        <span>
          {t('This will permanently delete rule')}{' '}
          <span className='font-semibold'>{deleteTarget?.name}</span>
          {t('. This action cannot be undone.')}
        </span>
      }
      destructive
      confirmText={t('Delete')}
      isLoading={deleteRuleMutation.isPending}
      handleConfirm={() => {
        if (deleteTarget) deleteRuleMutation.mutate(deleteTarget.id)
      }}
    />
    </>
  )
}

type RuleTableProps = {
  rules: ProxyRule[]
  loading: boolean
  onEdit: (rule: ProxyRule) => void
  onDelete: (rule: ProxyRule) => void
}

function RuleTable(props: RuleTableProps) {
  const { t } = useTranslation()

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Rules')}</CardTitle>
      </CardHeader>
      <CardContent className='overflow-x-auto'>
        {props.rules.length === 0 ? (
          <Empty className='border-0 py-8'>
            <EmptyTitle>
              {props.loading ? t('Loading...') : t('No rules yet')}
            </EmptyTitle>
            {!props.loading && (
              <EmptyDescription>
                {t('Create a rule to route channels through a proxy node')}
              </EmptyDescription>
            )}
          </Empty>
        ) : (
          <StaticDataTable
            tableClassName='min-w-max'
            data={props.rules}
            getRowKey={(rule) => rule.id}
            emptyContent={t('No rules yet')}
            columns={[
              {
                id: 'name',
                header: t('Name'),
                cellClassName: 'font-medium',
                cell: (rule) => rule.name,
              },
              {
                id: 'channels',
                header: t('Channels Bound'),
                cell: (rule) => rule.channel_ids.length,
              },
              {
                id: 'node',
                header: t('Node'),
                cell: (rule) => rule.node?.name || formatNodeAddress(rule.node),
              },
              {
                id: 'created',
                header: t('Created At'),
                cell: (rule) =>
                  rule.created_at
                    ? new Date(rule.created_at * 1000).toLocaleString()
                    : '—',
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (rule) => (
                  <div className='flex justify-end'>
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      title={t('Edit')}
                      onClick={() => props.onEdit(rule)}
                    >
                      <Pencil className='h-3 w-3' />
                    </Button>
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      title={t('Delete')}
                      onClick={() => props.onDelete(rule)}
                    >
                      <Trash2 className='h-3 w-3' />
                    </Button>
                  </div>
                ),
              },
            ]}
          />
        )}
      </CardContent>
    </Card>
  )
}