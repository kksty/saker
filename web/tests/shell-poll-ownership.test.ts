// One poll per shared endpoint.
//
// The shell owns the board (App.tsx) and the operator inbox (GraphView.tsx). A
// page that polled the same endpoint itself sent two requests per period, and
// the two copies could disagree for a moment (the rail badge vs the overview
// card). Pages receive the state as a prop and refresh through the shell's
// reload instead, so this test pins the ownership rather than a behaviour that
// only shows up in a network trace.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const read = (rel: string) =>
  readFile(fileURLToPath(new URL(`../src/${rel}`, import.meta.url)), 'utf8')

test('the board is polled once, in the shell', async () => {
  const app = await read('App.tsx')
  assert.match(app, /usePoll\(api\.board,/, 'App.tsx must own the board poll')
  for (const page of ['views/ProjectState.tsx', 'views/Messages.tsx', 'views/Overview.tsx']) {
    const source = await read(page)
    assert.doesNotMatch(source, /api\.board\b/, `${page} must render the shell's board, not poll it`)
  }
})

test('the operator inbox is polled once, in the shell', async () => {
  const shell = await read('views/GraphView.tsx')
  assert.match(shell, /usePoll\(api\.operatorInbox,/, 'GraphView must own the inbox poll')
  for (const page of ['views/Overview.tsx', 'views/Access.tsx']) {
    const source = await read(page)
    assert.doesNotMatch(source, /api\.operatorInbox\b/, `${page} must render the shell's inbox, not poll it`)
  }
})

test('the pages that used to poll receive the shell state as props', async () => {
  const shell = await read('views/GraphView.tsx')
  // Forwarding, not copying: the same AsyncState object reaches every page.
  assert.match(shell, /<Overview[\s\S]{0,400}?inbox=\{inbox\}/)
  assert.match(shell, /<Access inbox=\{inbox\} \/>/)
  assert.match(shell, /<StateCenter[\s\S]{0,400}?boardState=\{boardState\}/)
  assert.match(shell, /<Messages boardState=\{boardState\} \/>/)
  const center = await read('views/StateCenter.tsx')
  assert.match(center, /<ProjectState[\s\S]{0,200}?boardState=\{boardState\}/)
  // A manual refresh covers the inbox too, so 刷新 on any page cannot leave the
  // badge on an older read than the card next to it.
  assert.match(shell, /function refresh\(\)[\s\S]{0,400}?inbox\.reload\(\)/)
})
