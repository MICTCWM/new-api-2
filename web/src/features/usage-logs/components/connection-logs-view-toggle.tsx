import { LayoutGrid, Menu, Rows3 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import type { ConnectionViewMode } from './connection-logs-table'

const VIEW_MODES: Array<{
  mode: ConnectionViewMode
  /** English source string, translated through `t()`. */
  labelKey: string
  Icon: typeof Rows3
}> = [
  { mode: 'list', labelKey: 'List view', Icon: Rows3 },
  { mode: 'compact', labelKey: 'Compact', Icon: Menu },
  { mode: 'grid', labelKey: 'Grid view', Icon: LayoutGrid },
]

/**
 * Mobile-only density switch: two-line cards, single-line rows, or the token
 * matrix. Each button is icon-only so the control fits next to the refresh
 * action on a 320px viewport; the label stays in the accessible name.
 */
export function ConnectionLogsViewToggle(props: {
  value: ConnectionViewMode
  onChange: (mode: ConnectionViewMode) => void
}) {
  const { t } = useTranslation()

  return (
    <div
      role='group'
      aria-label={t('View mode')}
      className='bg-muted/50 flex shrink-0 items-center gap-0.5 rounded-md p-0.5'
    >
      {VIEW_MODES.map(({ mode, labelKey, Icon }) => {
        const selected = props.value === mode

        return (
          <Button
            key={mode}
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-pressed={selected}
            aria-label={t(labelKey)}
            title={t(labelKey)}
            className={cn(
              'size-6 rounded-sm',
              selected
                ? 'bg-background text-foreground hover:bg-background shadow-xs'
                : 'text-muted-foreground hover:text-foreground'
            )}
            onClick={() => props.onChange(mode)}
          >
            <Icon className='size-3' aria-hidden='true' />
          </Button>
        )
      })}
    </div>
  )
}
