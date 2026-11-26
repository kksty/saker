import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

// Layout guard for the chat meta row. A participant title can be a whole
// direction name; the delivery badge must not be pushed off the row, and the
// body must own the long text. These assertions pin the two structural rules:
// the identity labels truncate, and time + status travel together as one
// non-shrinking group.
const source = await readFile(fileURLToPath(new URL('../src/views/Messages.tsx', import.meta.url)), 'utf8')

test('identity labels truncate instead of widening the row', () => {
  assert.match(source, /className="min-w-0 max-w-\[14rem\] truncate font-medium text-ink-2" title=\{sender\}/)
  assert.match(source, /truncate', outgoing \? 'text-ink-3' : 'text-ink-2'\)\} title=\{recipient\}/)
})

test('time and delivery badge stay one shrink-0 group', () => {
  assert.match(source, /<span className="ml-auto flex shrink-0 items-center gap-1\.5">\s*\n\s*<time/)
  assert.match(source, /\{deliveryShortLabel\(message\.status\)\}/)
})

test('the bubble can wrap long text and never overflows', () => {
  assert.match(source, /saker-break min-w-0 max-w-full whitespace-pre-wrap break-words rounded-2xl border/)
})
