import { useId } from 'react'
import { cn } from '@cyber/theme'

/**
 * The Saker wordmark: five letters drawn on a 100-unit grid, not a typeset string.
 *
 * It is drawn because the mark has to look the same on a machine with no Kaiti
 * face as on one with three. The previous lockup was live text over a 楷体 stack
 * plus a gradient, so its shape was the operator's font inventory; this one is
 * geometry and renders identically in the 216px rail, the 52px compact header
 * and on the login card.
 *
 * The lettering is monoline technical sans: one 16-unit stroke for every stem,
 * arm and curve. S is two true curves cut near-vertically at the ends — a
 * right-angle S reads as the digit 5, which is what the earlier geometric draft
 * did. A has a flat crown and feet because a mitred apex becomes a needle at
 * small sizes; K's diagonals meet inside the stem so the mitre is covered; E's
 * middle arm is 5 units short; R's leg grows from the underside of the bowl.
 * Each glyph is clipped to its own box, so the letters sit flush on the cap line
 * and baseline.
 *
 * Below 96px wide the S curves start to blur, so that is the floor for this
 * drawing; the rail draws it at 120 and the compact header at 104.
 *
 * The login page in cmd/saker/auth.go carries the same path strings —
 * web/tests/brand-mark.test.ts fails if the two drift.
 */

/** Glyph box height in art units: every letter is clipped to it. */
export const WORDMARK_CAP = 100
/** Total ink width in art units: the last glyph ends at 301 + 66. */
export const WORDMARK_WIDTH = 367
/** One stroke weight for the whole mark. */
export const WORDMARK_STROKE = 16
/** Legibility floor in CSS pixels, from the artwork's own spec. */
export const WORDMARK_MIN_WIDTH = 96

export type BrandGlyph = {
  ch: string
  /** Horizontal position of the glyph box in art units. */
  offset: number
  /** Glyph box width in art units; also the clip width. */
  width: number
  /** Filled outline (A), drawn with the even-odd rule. */
  fill?: string
  /** Stroked centreline (S, K, E, R), drawn at WORDMARK_STROKE. */
  stroke?: string
}

export const WORDMARK_GLYPHS: BrandGlyph[] = [
  {
    ch: 'S',
    offset: 0,
    width: 66,
    stroke:
      'M58 22 C58 11 47 8 33 8 C18 8 8 14 8 26 C8 37 16 43 30 48 C45 53 55 59 55 71 C55 84 44 92 29 92 C14 92 8 86 8 74',
  },
  {
    ch: 'A',
    offset: 74,
    width: 68,
    fill: 'M0 100 L26 0 L42 0 L68 100 L52 100 L46.24 76 L21.76 76 L16 100 Z M34 25 L25.6 60 L42.4 60 Z',
  },
  {
    ch: 'K',
    offset: 153,
    width: 68,
    stroke: 'M8 0 L8 100 M72 0 L16 52 L72 100',
  },
  {
    ch: 'E',
    offset: 233,
    width: 56,
    stroke: 'M8 0 L8 100 M0 8 L56 8 M0 50 L48 50 M0 92 L56 92',
  },
  {
    ch: 'R',
    offset: 301,
    width: 66,
    stroke: 'M8 0 L8 100 M8 8 L32 8 C52 8 58 17 58 30 C58 44 50 52 32 52 L8 52 M34 56 L66 104',
  },
]

function Glyphs({ glyphs, idPrefix }: { glyphs: BrandGlyph[]; idPrefix: string }) {
  return (
    <>
      {glyphs.map(glyph => {
        const clip = `${idPrefix}-${glyph.ch}`
        return (
          <g key={glyph.ch} transform={`translate(${glyph.offset} 0)`}>
            <clipPath id={clip}>
              <rect x="0" y="0" width={glyph.width} height={WORDMARK_CAP} />
            </clipPath>
            <g clipPath={`url(#${clip})`}>
              {glyph.fill ? <path d={glyph.fill} fillRule="evenodd" /> : null}
              {glyph.stroke ? (
                <path
                  d={glyph.stroke}
                  fill="none"
                  strokeWidth={WORDMARK_STROKE}
                  strokeLinecap="butt"
                  strokeLinejoin="miter"
                  strokeMiterlimit={10}
                />
              ) : null}
            </g>
          </g>
        )
      })}
    </>
  )
}

/**
 * The full wordmark. `width` is the ink width in CSS pixels; the height follows
 * at the design ratio (100 / 367). Keep it at or above WORDMARK_MIN_WIDTH.
 */
export function Wordmark({ width = 120, className }: { width?: number; className?: string }) {
  const id = useId().replace(/:/g, '')
  const height = Math.round((width * WORDMARK_CAP) / WORDMARK_WIDTH)
  return (
    <svg
      role="img"
      aria-label="Saker"
      viewBox={`0 0 ${WORDMARK_WIDTH} ${WORDMARK_CAP}`}
      width={Math.round(width)}
      height={height}
      className={cn('saker-wordmark shrink-0', className)}
      fill="currentColor"
      stroke="currentColor"
    >
      <Glyphs glyphs={WORDMARK_GLYPHS} idPrefix={id} />
    </svg>
  )
}
