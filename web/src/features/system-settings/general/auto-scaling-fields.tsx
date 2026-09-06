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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import {
  useFormContext,
  useWatch,
  type Control,
} from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { getChannels } from '@/features/channels/api'
import type { Channel } from '@/features/channels/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import {
  SettingsControlChildren,
  SettingsControlGroup,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { getAutoscalingInstances, getAutoscalingTest } from './auto-scaling-api'
import type { AutoscalingInstance } from './auto-scaling-lib'
import {
  extractDeploymentCount,
  formatChannelIDs,
  formatInstanceIDs,
  parseChannelIDs,
  parseInstanceIDs,
} from './auto-scaling-lib'
import type { BehaviorFormValues } from './system-behavior-section'

type AutoScalingFieldsProps = {
  control: Control<BehaviorFormValues>
}

const EMPTY_CHANNELS: Channel[] = []
const EMPTY_INSTANCES: AutoscalingInstance[] = []

/**
 * "Auto Scaling" configuration block inside the System Behavior settings form.
 * The whole block lives in the same react-hook-form form as the rest of the
 * behavior settings so that only (key, value) pairs which actually change are
 * persisted through the shared onSubmit path.
 */
export function AutoScalingFields(props: AutoScalingFieldsProps) {
  const { t } = useTranslation()

  const autoScalingEnabled = useWatch({
    control: props.control,
    name: 'AutoScalingEnabled',
  })
  const autoScalingToken = useWatch({
    control: props.control,
    name: 'AutoScalingToken',
  })
  const autoScalingChannelIDs = useWatch({
    control: props.control,
    name: 'AutoScalingChannelIDs',
  })
  const autoScalingInstanceIDs = useWatch({
    control: props.control,
    name: 'AutoScalingInstanceIDs',
  })
  const { setValue } = useFormContext<BehaviorFormValues>()

  const channelsQuery = useQuery({
    queryKey: ['channels', 'autoscaling-select'],
    queryFn: () => getChannels({ page_size: 1000 }),
    staleTime: 5 * 60 * 1000,
  })

  const channels = channelsQuery.data?.data?.items ?? EMPTY_CHANNELS
  const selectedChannelIds = useMemo(
    () => parseChannelIDs(autoScalingChannelIDs),
    [autoScalingChannelIDs]
  )

  const setChannelChecked = (channelId: number, checked: boolean) => {
    const next = checked
      ? selectedChannelIds.includes(channelId)
        ? selectedChannelIds
        : [...selectedChannelIds, channelId]
      : selectedChannelIds.filter((id) => id !== channelId)
    setValue('AutoScalingChannelIDs', formatChannelIDs(next))
  }

  const instancesQuery = useQuery({
    queryKey: ['autoscaling-instances', autoScalingToken],
    queryFn: () => getAutoscalingInstances(),
    enabled: Boolean(autoScalingToken),
    staleTime: 5 * 60 * 1000,
    retry: false,
  })

  const instancesResult = instancesQuery.data
  const instances = instancesResult?.data?.instances ?? EMPTY_INSTANCES
  const selectedInstanceUuids = useMemo(
    () => parseInstanceIDs(autoScalingInstanceIDs),
    [autoScalingInstanceIDs]
  )

  const setInstanceChecked = (instanceUuid: string, checked: boolean) => {
    const next = checked
      ? selectedInstanceUuids.includes(instanceUuid)
        ? selectedInstanceUuids
        : [...selectedInstanceUuids, instanceUuid]
      : selectedInstanceUuids.filter((uuid) => uuid !== instanceUuid)
    setValue('AutoScalingInstanceIDs', formatInstanceIDs(next))
  }

  const instancesLoadFailed =
    instancesQuery.isError || (instancesResult != null && !instancesResult.success)

  const testConnectionMutation = useMutation({
    mutationFn: () =>
      getAutoscalingTest({
        token: autoScalingToken,
      }),
    onSuccess: (result) => {
      if (result?.success) {
        const deployments = extractDeploymentCount(result)
        if (deployments !== null) {
          toast.success(
            t('Auto scaling test succeeded: {{count}} deployments', {
              count: deployments,
            })
          )
        } else {
          toast.success(t('Auto scaling test succeeded'))
        }
        return
      }
      toast.error(result?.message || t('Auto scaling test failed'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Auto scaling test failed'))
    },
  })

  return (
    <SettingsControlGroup>
      <FormField
        control={props.control}
        name='AutoScalingEnabled'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Auto Scaling')}</FormLabel>
              <FormDescription>
                {t(
                  'Automatically expand capacity when the global requests per minute exceed the threshold'
                )}
              </FormDescription>
            </SettingsSwitchContent>
            <FormControl>
              <Switch
                checked={field.value}
                onCheckedChange={field.onChange}
              />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />

      {autoScalingEnabled ? (
        <SettingsControlChildren>
          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={props.control}
              name='AutoScalingRPMLimit'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('RPM threshold')}</FormLabel>
                  <FormDescription>
                    {t('Maximum requests per minute before expanding')}
                  </FormDescription>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      value={field.value}
                      onChange={(event) =>
                        field.onChange(event.target.valueAsNumber)
                      }
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
              control={props.control}
              name='AutoScalingCooldownMinutes'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Cooldown minutes')}</FormLabel>
                  <FormDescription>
                    {t('Delay between auto scaling actions in minutes')}
                  </FormDescription>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      value={field.value}
                      onChange={(event) =>
                        field.onChange(event.target.valueAsNumber)
                      }
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          <FormField
            control={props.control}
            name='AutoScalingToken'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('AutoDL API Token')}</FormLabel>
                <FormDescription>
                  {t('AutoDL API token used to authenticate requests')}
                </FormDescription>
                <FormControl>
                  <Input
                    type='password'
                    autoComplete='off'
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

          <FormItem>
            <FormLabel>{t('Auto Scaling Instances')}</FormLabel>
            <FormDescription>
              {t('Select instances as the auto-scaling targets')}
            </FormDescription>
            <div className='border-input grid max-h-48 gap-1.5 overflow-y-auto rounded-md border p-2'>
              {!autoScalingToken ? (
                <p className='text-muted-foreground px-1 py-1 text-xs'>
                  {t('No AutoDL token configured')}
                </p>
              ) : instancesLoadFailed ? (
                <p className='text-muted-foreground px-1 py-1 text-xs'>
                  {t('Failed to load instances')}
                </p>
              ) : instances.length === 0 ? (
                <p className='text-muted-foreground px-1 py-1 text-xs'>
                  {instancesQuery.isLoading
                    ? t('Loading...')
                    : t('No instances found')}
                </p>
              ) : (
                instances.map((instance) => {
                  const checked = selectedInstanceUuids.includes(
                    instance.instance_uuid
                  )
                  return (
                    <label
                      key={instance.instance_uuid}
                      className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm'
                    >
                      <Checkbox
                        checked={checked}
                        onCheckedChange={(checked) =>
                          setInstanceChecked(instance.instance_uuid, checked)
                        }
                        disabled={instancesQuery.isLoading}
                      />
                      <span className='min-w-0 flex-1 truncate'>
                        {instance.name?.trim() || instance.instance_uuid.slice(0, 12)}
                      </span>
                      <span className='text-muted-foreground flex-shrink-0 text-xs'>
                        {instance.status}
                      </span>
                    </label>
                  )
                })
              )}
            </div>
          </FormItem>

          <FormItem>
            <FormLabel>{t('Auto Scaling Channels')}</FormLabel>
            <FormDescription>
              {t('Select channels as the auto-scaling targets')}
            </FormDescription>
            <div className='border-input grid max-h-48 gap-1.5 overflow-y-auto rounded-md border p-2'>
              {channels.map((channel) => {
                const checked = selectedChannelIds.includes(channel.id)
                return (
                  <label
                    key={channel.id}
                    className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm'
                  >
                    <Checkbox
                      checked={checked}
                      onCheckedChange={(checked) =>
                        setChannelChecked(channel.id, checked)
                      }
                      disabled={channelsQuery.isLoading}
                    />
                    <span>
                      {channel.name ?? ''} (ID: {channel.id})
                    </span>
                  </label>
                )
              })}
              {channels.length === 0 ? (
                <p className='text-muted-foreground px-1 py-1 text-xs'>
                  {channelsQuery.isLoading
                    ? t('Loading...')
                    : t('No channels found')}
                </p>
              ) : null}
            </div>
          </FormItem>

          <div className='flex items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => testConnectionMutation.mutate()}
              disabled={testConnectionMutation.isPending}
            >
              {testConnectionMutation.isPending
                ? t('Testing connection...')
                : t('Test connection')}
            </Button>
          </div>
        </SettingsControlChildren>
      ) : (
        <p className='text-muted-foreground px-1 text-xs'>
          {t('Auto scaling disabled')}
        </p>
      )}
    </SettingsControlGroup>
  )
}