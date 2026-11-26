/**
 * The browser tab names the console: "Saker · 项目 · 当前面板". The shell builds
 * the parts it owns and leaves the rest to the view, so the same rule keeps the
 * tab readable whether the operator has one tab or ten open.
 */
export const APP_NAME = 'Saker'

/** ['客户 A','图谱'] → 'Saker · 客户 A · 图谱'；跳过空串/undefined，全空返回 'Saker'。 */
export function pageTitle(parts: (string | null | undefined)[]): string {
  const named = parts
    .map((part) => part?.trim())
    .filter((part): part is string => Boolean(part))
  return [APP_NAME, ...named].join(' · ')
}
