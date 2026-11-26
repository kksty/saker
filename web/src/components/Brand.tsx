import type { ReactNode } from 'react'
import { cn } from '@cyber/theme'

/**
 * 左上角品牌锁排: pure 「Saker」艺术字, no icon.
 *
 * The wordmark is deliberately typographic: 楷体/衬线字形 + 暖米→珊瑚渐变 +
 * 一道极淡的篆刻投影, with a small vermillion seal dot as the only graphic
 * accent. It is a lockup, not decoration: the name must read at a glance in the
 * 216px rail, and the SAKER caption at 9px is the latin fallback — it used to
 * read as a smudge at 7px beside the larger mark.
 *
 * The icon lives in the browser tab (`public/favicon.svg`): the rail stays
 * typographic, and the mark still owns the one square surface where a wordmark
 * cannot read.
 *
 * 占位钩子：正式 logo 素材由用户提供，到位后传 `logo`（替换左侧文字标记），
 * 或整份替换 `public/favicon.svg`；在此之前不自造图案。
 */
export function Brand({
  badge,
  logo,
  className,
}: {
  badge?: ReactNode
  /** 正式 logo 素材（如 <img src="/logo.svg" alt="Saker" />）；缺省沿用文字标记。 */
  logo?: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex min-w-0 items-center', className)}>
      {logo ? (
        <div className="flex min-w-0 items-center" data-brand-logo>
          {logo}
        </div>
      ) : (
        <div className="flex min-w-0 flex-col justify-center gap-[4px]">
          <span className="flex items-start gap-[2px]">
            <span className="saker-brand-cn">Saker</span>
            <span aria-hidden="true" className="saker-brand-dot" />
          </span>
          <span className="saker-brand-latin">SAKER</span>
        </div>
      )}
      {badge ? <div className="ml-auto shrink-0">{badge}</div> : null}
    </div>
  )
}
