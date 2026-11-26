// The standalone renderer also runs without a DOM. The console reads the same
// roles from app.css before drawing either canvas.
export const CANVAS = {
  background: '#0a0c10',
  surface: '#11151c',
  hairline: '#1b2129',
  ink: '#e7eaf0',
  ink2: '#a9b2c1',
  ink3: '#788294',
  primary: '#f2554d',
  success: '#3dd68c',
  warning: '#e5b53c',
  caution: '#f0883e',
  info: '#6cb6ff',
  review: '#2dd4bf',
  ai: '#b388ff',
  stopped: '#de93b2',
}

const tokens: Record<keyof typeof CANVAS, string> = {
  background: '--background',
  surface: '--card',
  hairline: '--hairline',
  ink: '--ink-1',
  ink2: '--ink-2',
  ink3: '--ink-3',
  primary: '--primary',
  success: '--success',
  warning: '--warning',
  caution: '--caution',
  info: '--info',
  review: '--review',
  ai: '--ai',
  stopped: '--stopped',
}

export function readGraphPalette(style: Pick<CSSStyleDeclaration, 'getPropertyValue'>): GraphPalette {
  const colors = { ...CANVAS }
  for (const role of Object.keys(tokens) as (keyof typeof tokens)[]) {
    colors[role] = style.getPropertyValue(tokens[role]).trim() || colors[role]
  }
  return graphPalette(colors)
}

function translucent(color: string, percent: number): string {
  return `color-mix(in srgb, ${color} ${percent}%, transparent)`
}

function graphPalette(colors: typeof CANVAS) {
  const facts: Record<string, string> = {
    vuln: colors.primary, cred: colors.ai, host: colors.info, service: colors.success,
    loot: colors.warning, artifact: colors.caution, note: colors.ink3,
  }
  const intents: Record<string, string> = {
    running: colors.primary, claimed: colors.warning, pending: colors.warning, waiting: colors.warning,
    reviewing: colors.review, done: colors.success, failed: colors.ai, review_failed: colors.caution,
    parked: colors.ink3, stopped: colors.stopped,
  }
  return {
    colors,
    facts,
    intents,
    edges: {
      produced: translucent(colors.primary, 45),
      brief: translucent(colors.ink3, 65),
      host: translucent(colors.ink3, 50),
      goal: translucent(colors.warning, 62),
      premise: translucent(colors.info, 72),
      premiseBlocked: translucent(colors.primary, 85),
      reference: translucent(colors.success, 82),
      pin: translucent(colors.success, 95),
      supports: translucent(colors.warning, 70),
    },
    boundaries: {
      not_loaded: colors.ink3,
      missing: translucent(colors.primary, 72),
      rejected: translucent(colors.ai, 72),
      withdrawn: translucent(colors.warning, 72),
    },
    goals: { met: colors.success, open: colors.warning },
    rings: {
      keyboard: translucent(colors.info, 95),
      review: translucent(colors.warning, 90),
      match: translucent(colors.primary, 70),
      activeMatch: translucent(colors.primary, 95),
      hover: translucent(colors.ink, 55),
      selection: translucent(colors.ink, 95),
    },
    originMarker: translucent(colors.ink, 90),
    helperMarker: translucent(colors.ink, 80),
    dots: translucent(colors.ink3, 18),
    band: translucent(colors.ink3, 5),
    divider: translucent(colors.ink3, 20),
    labelBg: translucent(colors.background, 90),
    labelInk: translucent(colors.ink, 92),
    sealed: `color-mix(in srgb, ${colors.success} 9%, ${colors.surface})`,
    minimapDim: translucent(colors.ink3, 26),
  }
}

export type GraphPalette = ReturnType<typeof graphPalette>
export const DEFAULT_GRAPH_PALETTE = graphPalette(CANVAS)
