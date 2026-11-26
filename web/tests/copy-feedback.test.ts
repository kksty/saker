// Copy feedback: a failed clipboard write is visible, and consecutive copies do
// not share one timer (the first confirmation must not erase the second).
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { act, create, type ReactTestRenderer } from 'react-test-renderer'
import { useCopyFeedback } from '../src/lib/use-copy-feedback.ts'

let renderer: ReactTestRenderer | undefined
let current: ReturnType<typeof useCopyFeedback>
const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator')

function setClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { clipboard: { writeText } },
  })
}

function Harness() {
  current = useCopyFeedback(50)
  return null
}

async function mount() {
  await act(async () => { renderer = create(React.createElement(Harness)) })
}

afterEach(async () => {
  await act(async () => { renderer?.unmount(); renderer = undefined })
  if (originalNavigator) Object.defineProperty(globalThis, 'navigator', originalNavigator)
  else Reflect.deleteProperty(globalThis, 'navigator')
})

test('a clipboard failure is reported and stays visible', async () => {
  setClipboard(async () => { throw new Error('denied') })
  await mount()
  let ok = true
  await act(async () => { ok = await current.copy('text', 'row') })
  assert.equal(ok, false)
  assert.deepEqual(current.feedback, { key: 'row', status: 'failed' })
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.deepEqual(current.feedback, { key: 'row', status: 'failed' }, 'a failure must not be cleared like a success')
})

test('consecutive copies keep their own confirmation timer', async () => {
  setClipboard(async () => {})
  await mount()
  await act(async () => { await current.copy('first', 'a') })
  assert.deepEqual(current.feedback, { key: 'a', status: 'ok' })
  await new Promise((resolve) => setTimeout(resolve, 25))
  await act(async () => { await current.copy('second', 'b') })
  assert.deepEqual(current.feedback, { key: 'b', status: 'ok' })
  // The first copy's timer would fire here; it must have been cancelled.
  await new Promise((resolve) => setTimeout(resolve, 30))
  assert.deepEqual(current.feedback, { key: 'b', status: 'ok' })
  await new Promise((resolve) => setTimeout(resolve, 40))
  assert.equal(current.feedback, null)
})
