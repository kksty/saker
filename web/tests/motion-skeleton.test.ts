// Boot skeleton contract.
//
// The Suspense fallback is the frame the next page will have (header strip, three
// metric cards, a list card), so the shell does not jump when the lazy chunk
// lands. It is deliberately *still*: entrance fades, a sweeping scan beam and
// staggered lifts used to live here, and they made a switch feel slower than it
// was — the content was already there, it just spent 200–260ms sliding in.
//
// This suite pins the stillness (no motion class, no inline motion override, no
// JS loop) and the geometry the page lands in. jsdom is not part of the test
// stack, so the component is rendered with react-test-renderer and inspected as
// a tree; the stylesheet side is pinned by fa3-app-css.
import { after, afterEach, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { createTestServer } from './helpers/vite.ts'

interface Node {
  type: string
  props: Record<string, unknown>
  children: (Node | string)[] | null
}

const sourcePath = fileURLToPath(new URL('../src/components/PageSkeleton.tsx', import.meta.url))

/** Every motion primitive this component used to opt into. */
const BANNED_MOTION = ['saker-rise', 'saker-stagger', 'saker-scan-track', 'saker-slide-in', 'saker-live', 'saker-breath', 'saker-ripple']

let server: Awaited<ReturnType<typeof createTestServer>>
let PageSkeleton: React.ComponentType
let renderer: ReactTestRenderer | undefined

before(async () => {
  server = await createTestServer({ root: fileURLToPath(new URL('..', import.meta.url)) })
  PageSkeleton = (await server.ssrLoadModule('/src/components/PageSkeleton.tsx')).PageSkeleton
})

afterEach(async () => {
  await act(async () => {
    renderer?.unmount()
    renderer = undefined
  })
})

after(async () => {
  await server?.close()
})

async function renderSkeleton(): Promise<Node> {
  await act(async () => {
    renderer = create(React.createElement(PageSkeleton))
  })
  return renderer!.toJSON() as unknown as Node
}

function classNames(node: Node): string[] {
  return typeof node.props.className === 'string' ? node.props.className.split(/\s+/) : []
}

function hasClass(node: Node, token: string): boolean {
  return classNames(node).includes(token)
}

function walk(node: Node | string, visit: (node: Node) => void) {
  if (typeof node === 'string') return
  visit(node)
  for (const child of node.children ?? []) walk(child, visit)
}

function findAll(root: Node, predicate: (node: Node) => boolean): Node[] {
  const found: Node[] = []
  walk(root, (node) => {
    if (predicate(node)) found.push(node)
  })
  return found
}

function elementChildren(node: Node): Node[] {
  return (node.children ?? []).filter((child): child is Node => typeof child !== 'string')
}

test('the fallback keeps the full-height Page frame and the loading semantics', async () => {
  const root = await renderSkeleton()
  assert.equal(root.props['aria-busy'], 'true')
  assert.equal(root.props['aria-label'], '页面加载中')
  for (const token of ['flex', 'h-full', 'min-h-0', 'flex-col']) {
    assert.ok(hasClass(root, token), `root keeps \`${token}\` so the lazy page does not jump`)
  }
})

test('nothing in the frame animates into place', async () => {
  const root = await renderSkeleton()
  walk(root, (node) => {
    for (const token of BANNED_MOTION) {
      assert.equal(hasClass(node, token), false, `\`${token}\` must not be used by the fallback`)
    }
    // No inline motion override either: a `--saker-i` delay or an `animation`
    // shorthand on the component would bypass the stylesheet contract.
    const style = node.props.style as Record<string, unknown> | undefined
    assert.equal(style, undefined, 'the fallback declares no inline style')
  })
})

test('the loading signal is the design system skeleton, and only that', async () => {
  const root = await renderSkeleton()
  const pulse = findAll(root, (node) => hasClass(node, 'animate-pulse'))
  assert.ok(pulse.length >= 4, 'the placeholder blocks are cyber-ui Skeletons')
  // A pulsing block is a loading affordance, not an entrance: none of them is
  // also carrying a motion class.
  for (const block of pulse) {
    for (const token of BANNED_MOTION) assert.equal(hasClass(block, token), false)
  }
})

test('the header, three metric cards and five-row list card keep their geometry', async () => {
  const root = await renderSkeleton()

  const header = findAll(root, (node) => hasClass(node, 'saker-page-header'))[0]
  assert.ok(header, 'the header strip keeps its saker-page-header chrome')
  assert.ok(hasClass(header, 'shrink-0'), 'header stays a fixed-height strip')
  const headerSkeletons = findAll(header, (node) => hasClass(node, 'animate-pulse'))
  assert.deepEqual(
    headerSkeletons.map((node) => classNames(node).filter((token) => !token.startsWith('bg-') && token !== 'animate-pulse' && token !== 'rounded-md')),
    [
      ['h-3', 'w-24'],
      ['h-7', 'w-28'],
      ['h-3.5', 'w-32', 'sm:w-72'],
      ['h-8', 'w-24'],
    ],
    'header placeholder lines and action keep their sizes',
  )

  const body = findAll(root, (node) => hasClass(node, 'flex-1'))[0]
  assert.ok(body, 'body keeps filling the remaining height')
  assert.ok(hasClass(body, 'min-h-0'), 'body keeps its scroll-safe flex basis')

  const grid = findAll(root, (node) => hasClass(node, 'grid'))[0]
  assert.ok(hasClass(grid, 'grid-cols-1'), 'cards stay a single column on narrow viewports')
  assert.ok(hasClass(grid, 'xl:grid-cols-3'), 'cards stay a three-up row on wide viewports')
  assert.ok(hasClass(grid, 'gap-4'))
  const cards = elementChildren(grid)
  assert.equal(cards.length, 3, 'three metric cards')
  for (const card of cards) {
    const skeletons = findAll(card, (node) => hasClass(node, 'animate-pulse'))
    assert.equal(skeletons.length, 1, 'one placeholder per metric card')
    assert.ok(hasClass(skeletons[0], 'h-20'), 'metric card keeps its 5rem height')
    assert.ok(hasClass(skeletons[0], 'rounded-xl'), 'metric card keeps its radius')
  }

  const listCard = findAll(root, (node) => hasClass(node, 'bg-card') && hasClass(node, 'border'))[0]
  assert.ok(listCard, 'the list card keeps its card chrome')
  assert.ok(hasClass(listCard, 'rounded-xl'))
  assert.ok(hasClass(listCard, 'p-4'))
  const rows = elementChildren(findAll(listCard, (node) => hasClass(node, 'space-y-2'))[0])
  assert.equal(rows.length, 5, 'the list keeps five placeholder rows')
})

test('the component starts no JS motion loop', () => {
  const source = readFileSync(sourcePath, 'utf8')
  for (const loop of ['setInterval', 'setTimeout', 'requestAnimationFrame']) {
    assert.equal(source.includes(loop), false, `a still skeleton must not need a JS ${loop} loop`)
  }
  // The design system owns the only animation (the Skeleton pulse); the
  // component never re-declares keyframes or durations of its own.
  assert.equal(source.includes('@keyframes'), false)
  assert.equal(source.includes('animation:'), false)
  assert.equal(source.includes('!important'), false)
})
