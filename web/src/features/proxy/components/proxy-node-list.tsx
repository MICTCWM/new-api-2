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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyTitle,
} from '@/components/ui/empty'
import { formatNodeAddress } from '../lib'
import type { ProxyNode } from '../types'

type ProxyNodeListProps = {
  nodes: ProxyNode[]
  loading: boolean
}

/**
 * Lists the nodes parsed from the proxy subscription. Non-bindable nodes are
 * shown greyed-out with a "protocol not supported" marker.
 */
export function ProxyNodeList(props: ProxyNodeListProps) {
  const { t } = useTranslation()

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t('Nodes')}
          <span className='text-muted-foreground ml-1.5 text-xs font-normal'>
            ({props.nodes.length})
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {props.nodes.length === 0 ? (
          <Empty className='border-0 py-8'>
            <EmptyTitle>
              {props.loading ? t('Loading...') : t('No nodes yet')}
            </EmptyTitle>
            {!props.loading && (
              <EmptyDescription>
                {t('Pull a subscription to discover nodes')}
              </EmptyDescription>
            )}
          </Empty>
        ) : (
          <ul className='grid gap-2'>
            {props.nodes.map((node, index) => {
              const unsupported = !node.bindable
              return (
                <li
                  key={`${node.name}-${index}`}
                  className={`flex items-center gap-2 rounded-md border px-3 py-2 text-sm ${
                    unsupported ? 'text-muted-foreground opacity-60' : ''
                  }`}
                >
                  <div className='flex min-w-0 flex-1 flex-col'>
                    <span className='truncate font-medium'>
                      {node.name || formatNodeAddress(node)}
                    </span>
                    <span className='text-muted-foreground truncate text-xs'>
                      {node.type} · {formatNodeAddress(node)}
                    </span>
                  </div>
                  {unsupported ? (
                    <Badge variant='outline' className='shrink-0'>
                      {t('Protocol not supported')}
                    </Badge>
                  ) : node.relay ? (
                    <Badge variant='secondary' className='shrink-0'>
                      {t('Xray relay')}
                    </Badge>
                  ) : (
                    <Badge variant='secondary' className='shrink-0'>
                      {t('Bindable')}
                    </Badge>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}