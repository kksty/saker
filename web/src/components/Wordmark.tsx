import { cn } from '@cyber/theme'

/**
 * The Saker wordmark: art lettering, not a typeset string.
 *
 * It is drawn because the mark has to look the same on a machine with no Kaiti
 * face as on one with three: the previous lockup was live text over a 楷体 stack
 * plus a gradient, so its shape was the operator's font inventory. This one is
 * geometry — reverse contrast (thick horizontals, thin stems), knife-cut free
 * ends — and reads identically at 15px in the 216px rail and at 44px on the
 * login card.
 *
 * No icon: the name is the mark. The one square surface that cannot hold a
 * wordmark (the browser tab) uses the S alone, drawn at the same weight.
 *
 * Geometry table: cap height 22, thick 5.2, thin 2.6, cut 2.9, tracking 4.4.
 * `public/favicon.svg` and the login page in cmd/saker/auth.go carry the same
 * point/stroke strings — web/tests/brand-mark.test.ts fails if the three drift.
 */

export const WORDMARK_CAP = 22
export const WORDMARK_TRACKING = 4.4
export const WORDMARK_WIDTH = 114

export type BrandGlyph = {
  ch: string
  offset: number
  polys: string[]
  strokes: { d: string; w: number }[]
}

export const WORDMARK_GLYPHS: BrandGlyph[] = [
  {
    ch: 'S',
    offset: 0,
    polys: [
      '0,0 18.6,0 15.7,5.2 0,5.2',
      '0,0 2.6,0 2.6,13.6 0,13.6',
      '0,8.4 18.6,8.4 18.6,13.6 0,13.6',
      '16,8.4 18.6,8.4 18.6,22 16,22',
      '2.9,16.8 18.6,16.8 18.6,22 0,22',
    ],
    strokes: [],
  },
  {
    ch: 'A',
    offset: 23,
    polys: ['6,12.6 17.7,12.6 14.8,17.8 3.1,17.8'],
    strokes: [
      { d: 'M10.4 0 L0.8 22', w: 3.6 },
      { d: 'M10.4 0 L20 22', w: 3.6 },
    ],
  },
  {
    ch: 'K',
    offset: 48.2,
    polys: ['0,0 2.6,0 2.6,22 0,22'],
    strokes: [
      { d: 'M19.2 0 L2.4 11.2', w: 4.5 },
      { d: 'M6.5 8.2 L19.6 22', w: 4.5 },
    ],
  },
  {
    ch: 'E',
    offset: 72.4,
    polys: [
      '0,0 2.6,0 2.6,22 0,22',
      '0,0 17.4,0 14.5,5.2 0,5.2',
      '0,8.4 15.2,8.4 12.3,13.6 0,13.6',
      '0,16.8 17.4,16.8 14.5,22 0,22',
    ],
    strokes: [],
  },
  {
    ch: 'R',
    offset: 94.2,
    polys: [
      '0,0 2.6,0 2.6,22 0,22',
      '0,0 19.8,0 16.9,5.2 0,5.2',
      '17.2,0 19.8,0 19.8,13.6 17.2,13.6',
      '0,8.4 19.8,8.4 16.9,13.6 0,13.6',
    ],
    strokes: [{ d: 'M11.4 13.6 L19.8 22', w: 4.7 }],
  },
]

/** The S alone — the tab icon and any square surface. */
export const MONOGRAM_GLYPHS: BrandGlyph[] = [
  { ...WORDMARK_GLYPHS[0], offset: 0 },
]

/** Padding (in glyph units) around the drawing box so knife cuts are not clipped. */
const PAD = 2

function Glyphs({ glyphs }: { glyphs: BrandGlyph[] }) {
  return (
    <>
      {glyphs.map(glyph => (
        <g key={`${glyph.ch}-${glyph.offset}`} transform={`translate(${glyph.offset} 0)`}>
          {glyph.polys.map(points => (
            <polygon key={points} points={points} />
          ))}
          {glyph.strokes.map(stroke => (
            <path key={stroke.d} d={stroke.d} strokeWidth={stroke.w} strokeLinecap="butt" />
          ))}
        </g>
      ))}
    </>
  )
}

/**
 * The full wordmark. `cap` is the drawn cap height in CSS pixels; the width
 * follows at the design ratio (114 / 22).
 */
export function Wordmark({ cap = 20, className }: { cap?: number; className?: string }) {
  const scale = cap / WORDMARK_CAP
  return (
    <svg
      role="img"
      aria-label="Saker"
      viewBox={`${-PAD} ${-PAD} ${WORDMARK_WIDTH + PAD * 2} ${WORDMARK_CAP + PAD * 2}`}
      width={Math.round((WORDMARK_WIDTH + PAD * 2) * scale)}
      height={Math.round((WORDMARK_CAP + PAD * 2) * scale)}
      className={cn('saker-wordmark shrink-0', className)}
      fill="currentColor"
      stroke="currentColor"
    >
      <Glyphs glyphs={WORDMARK_GLYPHS} />
    </svg>
  )
}

/** The S alone, for a square surface (the drawer title, a future app tile). */
export function Monogram({ cap = 18, className }: { cap?: number; className?: string }) {
  const scale = cap / WORDMARK_CAP
  const width = WORDMARK_GLYPHS[0].polys.reduce((widest, points) => {
    const xs = points.split(' ').map(pair => Number(pair.split(',')[0]))
    return Math.max(widest, ...xs)
  }, 0)
  return (
    <svg
      role="img"
      aria-label="Saker"
      viewBox={`${-PAD} ${-PAD} ${width + PAD * 2} ${WORDMARK_CAP + PAD * 2}`}
      width={Math.round((width + PAD * 2) * scale)}
      height={Math.round((WORDMARK_CAP + PAD * 2) * scale)}
      className={cn('saker-wordmark shrink-0', className)}
      fill="currentColor"
      stroke="currentColor"
    >
      <Glyphs glyphs={MONOGRAM_GLYPHS} />
    </svg>
  )
}
