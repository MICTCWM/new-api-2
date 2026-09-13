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
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery } from '@tanstack/react-query'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerFooter,
  DrawerHeader,
  DrawerTitle,
} from '@/components/ui/drawer'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  RadioGroup,
  RadioGroupItem,
} from '@/components/ui/radio-group'
import { getChannels } from '@/features/channels/api'
import type {
  CreateProxyRuleParams,
  ProxyNode,
  ProxyRule,
} from '../types'
import {
  formatNodeAddress,
  PROXY_RULE_FORM_SCHEMA,
  type ProxyRuleFormValues,
} from '../lib'
import { useForm } from 'react-hook-form'
import { useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

type RuleMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  nodes: ProxyNode[]
  isPending: boolean
  /** 非 null 表示编辑该规则，否则为新建。 */
  editing: ProxyRule | null
  onSubmit: (params: CreateProxyRuleParams) => void
}

const EMPTY_CHANNELS: { id: number; name?: string }[] = []

/** 判断两个节点是否为同一节点：优先比较分享链接，回退到名称+协议+地址。 */
function isSameNode(a: ProxyNode, b: ProxyNode): boolean {
  if (a.share_url && b.share_url) return a.share_url === b.share_url
  return (
    a.name === b.name &&
    a.type === b.type &&
    a.server === b.server &&
    a.port === b.port
  )
}

/**
 * "New / Edit rule" drawer: name + channel multi-select + a single proxy node.
 * The channel list mirrors the auto-scaling multi-checkbox pattern, and the
 * node list only allows bindable nodes (non-bindable ones are shown disabled
 * with a "protocol not supported" hint). When editing, the rule's node is
 * prepended to the list if it no longer exists in the subscription cache, so
 * the current selection always renders as a valid option.
 */
export function RuleMutateDrawer(props: RuleMutateDrawerProps) {
  const { t } = useTranslation()
  const editing = props.editing

  const form = useForm<ProxyRuleFormValues>({
    resolver: zodResolver(PROXY_RULE_FORM_SCHEMA),
    defaultValues: {
      name: '',
      channel_ids: [],
      nodeIndex: -1,
    },
  })

  // 打开时按编辑目标重置表单；编辑时把已选节点定位到列表下标。
  useEffect(() => {
    if (!props.open) return
    if (!editing) {
      form.reset({ name: '', channel_ids: [], nodeIndex: -1 })
      return
    }
    form.reset({
      name: editing.name,
      channel_ids: [...editing.channel_ids],
      nodeIndex: -1,
    })
    // nodeIndex 依赖 displayNodes，在 reset 后单独回填。
    const index = displayNodes.findIndex((node) =>
      isSameNode(node, editing.node)
    )
    if (index >= 0) {
      form.setValue('nodeIndex', index)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open, editing])

  const channelsQuery = useQuery({
    queryKey: ['channels', 'proxy-rule-select'],
    queryFn: () => getChannels({ page_size: 1000 }),
    staleTime: 5 * 60 * 1000,
  })
  const channels = channelsQuery.data?.data?.items ?? EMPTY_CHANNELS

  // 编辑时若规则所选节点已不在订阅缓存里（订阅刷新后被移除），
  // 把该节点插到列表最前，保证当前选择可见且可保持。
  const displayNodes = useMemo(() => {
    if (!editing) return props.nodes
    const exists = props.nodes.some((node) => isSameNode(node, editing.node))
    return exists ? props.nodes : [editing.node, ...props.nodes]
  }, [props.nodes, editing])

  const toggleChannel = (
    fieldValue: number[],
    onChange: (value: number[]) => void,
    channelId: number,
    checked: boolean
  ) => {
    onChange(
      checked
        ? fieldValue.includes(channelId)
          ? fieldValue
          : [...fieldValue, channelId]
        : fieldValue.filter((id) => id !== channelId)
    )
  }

  const handleSubmit = (values: ProxyRuleFormValues) => {
    const node = displayNodes[values.nodeIndex]
    if (!node) return
    props.onSubmit({
      name: values.name,
      channel_ids: values.channel_ids,
      node,
    })
  }

  return (
    <Drawer open={props.open} onOpenChange={props.onOpenChange} direction='right'>
      <DrawerContent className='w-full max-w-md sm:max-w-md'>
        <DrawerHeader>
          <DrawerTitle>
            {editing ? t('Edit Proxy Rule') : t('New Proxy Rule')}
          </DrawerTitle>
          <DrawerDescription>
            {t('Bind channels to a proxy node for this rule')}
          </DrawerDescription>
        </DrawerHeader>

        <Form {...form}>
          <form
            id='proxy-rule-form'
            onSubmit={form.handleSubmit(handleSubmit)}
            className='flex min-w-0 flex-col gap-4 px-4 pb-4'
          >
            <FormField
              control={form.control}
              name='name'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Rule Name')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder={t('proxy rule name')}
                      value={field.value}
                      onChange={field.onChange}
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='channel_ids'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Channels')}</FormLabel>
                  <div className='border-input grid max-h-48 gap-1.5 overflow-y-auto rounded-md border p-2'>
                    {channels.length === 0 ? (
                      <p className='text-muted-foreground px-1 py-1 text-xs'>
                        {channelsQuery.isLoading
                          ? t('Loading...')
                          : t('No channels found')}
                      </p>
                    ) : (
                      channels.map((channel) => (
                        <label
                          key={channel.id}
                          className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm'
                        >
                          <Checkbox
                            checked={field.value.includes(channel.id)}
                            onCheckedChange={(checked) =>
                              toggleChannel(
                                field.value,
                                field.onChange,
                                channel.id,
                                checked === true
                              )
                            }
                            disabled={channelsQuery.isLoading}
                          />
                          <span className='min-w-0 flex-1 truncate'>
                            {channel.name ?? ''} (ID: {channel.id})
                          </span>
                        </label>
                      ))
                    )}
                  </div>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='nodeIndex'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Proxy Node')}</FormLabel>
                  {displayNodes.length === 0 ? (
                    <p className='text-muted-foreground px-1 py-1 text-xs'>
                      {t('No proxy nodes available')}
                    </p>
                  ) : (
                    <RadioGroup
                      className='border-input grid max-h-48 gap-1 overflow-y-auto rounded-md border p-2'
                      value={String(field.value)}
                      onValueChange={(value) => {
                        if (value !== null) field.onChange(Number(value))
                      }}
                    >
                      {displayNodes.map((node, index) => {
                        const unsupported = !node.bindable
                        const isEditingNode =
                          editing !== null && isSameNode(node, editing.node)
                        return (
                          <label
                            key={`${node.name}-${index}`}
                            className={`flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm ${
                              unsupported ? 'cursor-not-allowed opacity-60' : 'hover:bg-muted/50'
                            }`}
                          >
                            <RadioGroupItem
                              value={String(index)}
                              disabled={unsupported}
                            />
                            <span className='min-w-0 flex-1'>
                              <span className='block truncate font-medium'>
                                {node.name || formatNodeAddress(node)}
                                {isEditingNode && (
                                  <span className='text-muted-foreground ml-1 text-xs'>
                                    ({t('Current')})
                                  </span>
                                )}
                              </span>
                              <span className='text-muted-foreground block truncate text-xs'>
                                {node.type} · {formatNodeAddress(node)}
                              </span>
                            </span>
                            {unsupported && (
                              <span className='text-muted-foreground flex-shrink-0 text-xs'>
                                {t('Protocol not supported')}
                              </span>
                            )}
                          </label>
                        )
                      })}
                    </RadioGroup>
                  )}
                  <FormMessage />
                </FormItem>
              )}
            />
          </form>
        </Form>

        <DrawerFooter>
          <Button
            variant='outline'
            onClick={() => props.onOpenChange(false)}
            disabled={props.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='proxy-rule-form'
            disabled={props.isPending}
          >
            {props.isPending
              ? editing
                ? t('Saving...')
                : t('Creating...')
              : editing
                ? t('Save')
                : t('Create')}
          </Button>
        </DrawerFooter>
      </DrawerContent>
    </Drawer>
  )
}
