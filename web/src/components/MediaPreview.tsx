import { useState } from 'react'
import { Download, ImageOff } from 'lucide-react'
import { Button, Dialog, DialogContent, DialogDescription, DialogTitle } from '@cyber/ui'
import { api } from '@/lib/api'
import { mediaKind, mediaKindLabel, mediaPathName } from '@/lib/run-media'

/**
 * One preserved medium (kind=media event). Images render as a thumbnail that
 * opens enlarged; audio/video get a native player with the URL as fallback
 * source; everything else is a link. If the GET fails (file swept, broken
 * permission, non-previewable type) the fallback is an ordinary link, never a
 * silently broken image — the operator can still download the bytes.
 */
export function MediaPreview({ runID, path }: { runID: string; path: string }) {
  const name = mediaPathName(path)
  const [failed, setFailed] = useState(false)
  const [zoom, setZoom] = useState(false)
  if (!name) {
    return (
      <p className="saker-break font-mono text-[11px] text-ink-3" title={path}>
        媒体路径无法解析：{path}
      </p>
    )
  }
  const kind = mediaKind(name)
  const url = api.runMediaURL(runID, name)
  const link = (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1 rounded text-[11px] text-info underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      <Download className="h-3 w-3" aria-hidden="true" />
      {failed ? '预览失败，打开媒体' : '打开媒体'}
      <span className="font-mono text-ink-3">{name}</span>
    </a>
  )
  if (kind !== 'image') {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[11px] text-ink-3">{mediaKindLabel(kind)}媒体</span>
        {kind === 'audio' && !failed && (
          <audio controls preload="none" src={url} onError={() => setFailed(true)} className="h-8 max-w-full" />
        )}
        {kind === 'video' && !failed && (
          <video controls preload="none" src={url} onError={() => setFailed(true)} className="max-h-48 max-w-full rounded border border-hairline" />
        )}
        {link}
      </div>
    )
  }
  if (failed) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <ImageOff className="h-3.5 w-3.5 text-warning" aria-hidden="true" />
        <span className="text-[11px] text-ink-3">图片无法预览</span>
        {link}
      </div>
    )
  }
  return (
    <div className="flex flex-wrap items-start gap-2">
      <button
        type="button"
        onClick={() => setZoom(true)}
        aria-label={`放大查看媒体 ${name}`}
        className="group relative overflow-hidden rounded border border-hairline bg-surface-2/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <img
          src={url}
          alt={`运行媒体 ${name}`}
          loading="lazy"
          decoding="async"
          onError={() => setFailed(true)}
          className="h-24 w-auto max-w-[240px] object-contain transition-opacity group-hover:opacity-90"
        />
      </button>
      <div className="flex min-w-0 flex-col gap-1">
        <span className="font-mono text-[11px] text-ink-3">{name}</span>
        {link}
      </div>
      <Dialog open={zoom} onOpenChange={setZoom}>
        <DialogContent className="max-h-[92dvh] max-w-[calc(100%_-_2rem)] overflow-auto overscroll-contain sm:max-w-4xl">
          <DialogTitle className="font-mono text-[12px] text-ink-2">{name}</DialogTitle>
          <DialogDescription className="sr-only">运行取得的媒体原件，放大查看。</DialogDescription>
          <img src={url} alt={`运行媒体 ${name}（放大）`} className="mx-auto max-h-[80dvh] w-auto max-w-full object-contain" />
          <div className="flex justify-end">
            <Button variant="outline" size="sm" onClick={() => setZoom(false)}>关闭</Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
