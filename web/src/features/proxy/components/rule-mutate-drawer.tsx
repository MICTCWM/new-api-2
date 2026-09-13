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
import type { CreateProxyRuleParams, ProxyNode } from '../types'
import {
  formatNodeAddress,
  PROXY_RULE_FORM_SCHEMA,
  type ProxyRuleFormValues,
} from '../lib'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

type RuleMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  nodes: ProxyNode[]
  isPending: boolean
  onCreate: (params: CreateProxyRuleParams) => void
}

const EMPTY_CHANNELS: { id: number; name?: string }[] = []

/**
 * "New rule" drawer: name + channel multi-select + a single proxy node.
 * The channel list mirrors the auto-scaling multi-checkbox pattern, and the
 * node list only allows bindable nodes (non-bindable ones are shown disabled
 * with a "protocol not supported" hint).
 */
export function RuleMutateDrawer(props: RuleMutateDrawerProps) {
  const { t } = useTranslation()

  const form = useForm<ProxyRuleFormValues>({
    resolver: zodResolver(PROXY_RULE_FORM_SCHEMA),
    defaultValues: {
      name: '',
      channel_ids: [],
      nodeIndex: -1,
    },
  })

  const channelsQuery = useQuery({
    queryKey: ['channels', 'proxy-rule-select'],
    queryFn: () => getChannels({ page_size: 1000 }),
    staleTime: 5 * 60 * 1000,
  })
  const channels = channelsQuery.data?.data?.items ?? EMPTY_CHANNELS

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
    const node = props.nodes[values.nodeIndex]
    if (!node) return
    props.onCreate({
      name: values.name,
      channel_ids: values.channel_ids,
      node,
    })
  }

  return (
    <Drawer open={props.open} onOpenChange={props.onOpenChange} direction='right'>
      <DrawerContent className='w-full max-w-md sm:max-w-md'>
        <DrawerHeader>
          <DrawerTitle>{t('New Proxy Rule')}</DrawerTitle>
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
                  {props.nodes.length === 0 ? (
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
                      {props.nodes.map((node, index) => {
                        const unsupported = !node.bindable
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
            {props.isPending ? t('Creating...') : t('Create')}
          </Button>
        </DrawerFooter>
      </DrawerContent>
    </Drawer>
  )
}