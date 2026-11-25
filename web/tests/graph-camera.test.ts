// Camera-rule tests for the console graph. The map used to fit itself to the
// canvas whenever the ledger was rebuilt (a poll every few seconds), which
// yanked the view away from the node the operator was reading. These tests pin
// the replacement rules: open at a fixed zoom centred on the layout, never
// scale to fit except through the explicit 适配画布 action, and bring back the
// operator's last pan/zoom after a reload or a visit to another panel.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  OPEN_ZOOM,
  cameraStorageKey,
  focusCamera,
  layoutBounds,
  openCamera,
  parseCamera,
  type NodeExtent,
} from '../src/graph/camera.ts'

function extent(x: number, y: number, halfW = 37, halfH = 37): NodeExtent {
  return { x, y, halfW, halfH }
}

test('openCamera keeps a fixed zoom instead of shrinking a large graph to fit the canvas', () => {
  const small = [extent(0, 0), extent(120, 60)]
  const huge = [extent(0, 0), extent(9000, 4000)]
  const a = openCamera(small, 800, 600)
  const b = openCamera(huge, 800, 600)
  assert.ok(a && b)
  assert.equal(a.k, OPEN_ZOOM)
  assert.equal(b.k, OPEN_ZOOM)
})

test('openCamera centres the drawn layout in the viewport', () => {
  const cam = openCamera([extent(100, 0), extent(300, 40)], 800, 600)
  assert.ok(cam)
  assert.equal(cam.k, OPEN_ZOOM)
  assert.equal(cam.x, 800 / 2 - 200 * OPEN_ZOOM)
  assert.equal(cam.y, 600 / 2 - 20 * OPEN_ZOOM)
})

test('openCamera waits until both a layout and a canvas exist', () => {
  assert.equal(openCamera([], 800, 600), null)
  assert.equal(openCamera([extent(0, 0)], 0, 600), null)
  assert.equal(openCamera([extent(0, 0)], 800, 0), null)
})

test('layoutBounds spans every drawn extent', () => {
  const bounds = layoutBounds([extent(100, 20, 10, 5), extent(-40, -60, 8, 12)])
  assert.ok(bounds)
  assert.equal(bounds.minX, -48)
  assert.equal(bounds.maxX, 110)
  assert.equal(bounds.minY, -72)
  assert.equal(bounds.maxY, 25)
  assert.equal(layoutBounds([]), null)
})

test('parseCamera accepts only a well-formed saved view', () => {
  assert.deepEqual(parseCamera('{"x":12,"y":-3,"k":1.5}'), { x: 12, y: -3, k: 1.5 })
  assert.equal(parseCamera(null), null)
  assert.equal(parseCamera(''), null)
  assert.equal(parseCamera('not json'), null)
  assert.equal(parseCamera('null'), null)
  assert.equal(parseCamera('{"x":1,"y":2}'), null)
  assert.equal(parseCamera('{"x":1,"y":2,"k":0}'), null)
  assert.equal(parseCamera('{"x":1,"y":2,"k":-2}'), null)
  assert.equal(parseCamera('{"x":"1","y":2,"k":1}'), null)
})

test('focusCamera centres a search hit in the band the floating cards leave visible', () => {
  // 1000x600 canvas, filter card 200px on the left, node panel 300px on the
  // right, bottom stack 100px: the visible band is x 200..700, y 0..500.
  const cam = focusCamera({ x: 100, y: 50 }, 1000, 600, 1, { left: 200, right: 300, bottom: 100 })
  assert.ok(cam)
  assert.equal(cam.k, 1)
  assert.equal(cam.x, 450 - 100, 'the hit sits at the band centre, not the canvas centre')
  assert.equal(cam.y, 250 - 50)
})

test('focusCamera raises a zoomed-out map to a readable zoom but keeps a closer view', () => {
  assert.equal(focusCamera({ x: 0, y: 0 }, 800, 600, 0.3)?.k, 1)
  assert.equal(focusCamera({ x: 0, y: 0 }, 800, 600, 2.4)?.k, 2.4)
  assert.equal(focusCamera({ x: 0, y: 0 }, 800, 600, 9)?.k, 4, 'the canvas zoom cap still applies')
  assert.equal(focusCamera({ x: 0, y: 0 }, 800, 600, 0.01)?.k, 1, 'minZoom wins over an absurd current zoom')
})

test('focusCamera clamps insets and refuses a missing canvas', () => {
  // A panel may cover at most 60% of an axis; beyond that the node would be
  // centred in a band that is not on screen at all.
  const cam = focusCamera({ x: 0, y: 0 }, 1000, 600, 1, { left: 5000, right: 5000 })
  assert.ok(cam)
  assert.equal(cam.x, 600.5, 'left is capped at 600 and the right inset only shrinks the band')
  assert.equal(cam.y, 300)
  assert.equal(focusCamera({ x: 0, y: 0 }, 0, 600, 1), null)
  assert.equal(focusCamera({ x: 0, y: 0 }, 800, 0, 1), null)
  assert.equal(focusCamera({ x: Number.NaN, y: 0 }, 800, 600, 1), null)
})

test('cameraStorageKey keeps one camera per project', () => {
  assert.notEqual(cameraStorageKey('proj-a'), cameraStorageKey('proj-b'))
  assert.match(cameraStorageKey('proj-a'), /proj-a/)
})
