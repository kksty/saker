// 「一个坏项目目录顶掉整个实例启动」修好之后的界面契约。
//
// 加载器现在跳过坏目录、继续服务其它项目，并把跳过的目录放进
// GET /api/projects 的 skipped_projects。项目列表页必须把它渲染成一条
// **常显**警示：项目 ID、目录绝对路径、失败原因，并说明其它项目正常、目录
// 没有被删改。不能做成弹窗、不能自动消失——项目在修好之前一直是缺的，
// 提示也就得一直在。
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createTestServer } from './helpers/vite.ts'
import type { ProjectInfo, SkippedProject } from '../src/lib/types.ts'

const skipped: SkippedProject[] = [
  {
    id: 'p_broken',
    dir: 'D:\\saker-data\\projects\\p_broken',
    error: 'requires a current SQLite ledger: open D:\\saker-data\\projects\\p_broken\\ledger.sqlite: no such file',
  },
]

const healthy: ProjectInfo = {
  id: 'p_ok',
  name: '正常项目',
  created: '2026-11-23T10:00:00+08:00',
  intents: 2,
  facts: 5,
  evidence: 3,
  running: 0,
  max_runs_effective: 4,
} as ProjectInfo

let server: Awaited<ReturnType<typeof createTestServer>>
let Notice: React.ComponentType<{ skipped: SkippedProject[] }>
let Page: React.ComponentType<any>

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  const module = await server.ssrLoadModule('/src/views/Projects.tsx')
  Notice = module.SkippedProjectsNotice
  Page = module.Projects
})

after(async () => {
  await server?.close()
})

test('a skipped directory is rendered with its id, path and reason, and stays put', () => {
  const html = renderToStaticMarkup(React.createElement(Notice, { skipped }))
  assert.match(html, /1 个项目目录没加载/)
  assert.match(html, /p_broken/)
  assert.match(html, /D:\\saker-data\\projects\\p_broken/)
  assert.match(html, /requires a current SQLite ledger/)
  // Says the rest of the instance is fine, and that nothing was done to the dir.
  assert.match(html, /其它项目照常启动与读写/)
  assert.match(html, /不删、不改、也不新建空账本/)
  // Nothing that hides it: no dismiss/close control in the notice.
  assert.doesNotMatch(html, /收起/)
  assert.doesNotMatch(html, /关闭/)
})

test('a healthy instance renders no notice at all', () => {
  assert.equal(renderToStaticMarkup(React.createElement(Notice, { skipped: [] })), '')
})

test('the notice sits above the list, so the short count is explained first', () => {
  const html = renderToStaticMarkup(
    React.createElement(Page, {
      projects: [healthy],
      skipped,
      current: '',
      onChanged: () => {},
      onDeleted: () => {},
      refreshing: false,
      loading: false,
    }),
  )
  const noticeAt = html.indexOf('个项目目录没加载')
  // The heading itself, not the refresh button's aria-label ("刷新项目列表"),
  // which lives in the page header above the body.
  const listAt = html.indexOf('id="project-list-heading"')
  assert.ok(noticeAt >= 0, 'the notice is rendered on the projects page')
  assert.ok(listAt >= 0, 'the project list is rendered')
  assert.ok(noticeAt < listAt, 'the notice comes before the list heading')
  // The healthy project is still listed next to the warning.
  assert.match(html, /正常项目/)
})
