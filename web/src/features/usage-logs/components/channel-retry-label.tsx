import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

interface ChannelRetryLabelProps {
  /** Same-channel retries the request spent; zero means none. */
  count: number
  className?: string
}

export function ChannelRetryLabel(props: ChannelRetryLabelProps) {
  const { t } = useTranslation()
  const retried = props.count > 0

  return (
    <span
      data-slot='channel-retry-label'
      className={cn(
        'shrink-0 text-[11px] leading-none whitespace-nowrap',
        retried ? 'text-warning' : 'text-muted-foreground',
        props.className
      )}
    >
      {retried
        ? t('Retried {{count}} time(s)', { count: props.count })
        : t('No retry')}
    </span>
  )
}
