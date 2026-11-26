// Preserved-media events (kind=media): the journal row's Text is a case-relative
// path below .saker/media (see internal/aopexec/media.go). The console reads it
// back through GET /api/runs/{id}/media/{name}, whose handler only accepts one
// plain file name — so the path is reduced to its basename here.

export type MediaKind = 'image' | 'audio' | 'video' | 'pdf' | 'text' | 'other'

const mediaPath = /(?:^|\/)\.saker\/media\/([A-Za-z0-9._-]+)\s*$/

/**
 * The generated media file name from a journal path, or '' when the text is not
 * one. The file-name shape is the one aopexec.PreserveMedia writes; anything
 * else (a URI, a directory, a user path, a traversal attempt) is refused rather
 * than turned into an URL the server would 404.
 */
export function mediaPathName(text?: string | null): string {
  if (!text) return ''
  const match = mediaPath.exec(text.trim())
  const name = match?.[1] ?? ''
  if (!name || name === '.' || name === '..') return ''
  return name
}

export function mediaKind(name: string): MediaKind {
  const dot = name.lastIndexOf('.')
  const ext = dot >= 0 ? name.slice(dot + 1).toLowerCase() : ''
  switch (ext) {
    case 'png':
    case 'jpg':
    case 'jpeg':
    case 'gif':
    case 'webp':
    case 'bmp':
    case 'tiff':
    case 'svg':
      return 'image'
    case 'mp3':
    case 'wav':
    case 'ogg':
    case 'm4a':
      return 'audio'
    case 'mp4':
    case 'webm':
      return 'video'
    case 'pdf':
      return 'pdf'
    case 'txt':
    case 'json':
    case 'html':
    case 'xml':
      return 'text'
    default:
      return 'other'
  }
}

export function mediaKindLabel(kind: MediaKind): string {
  switch (kind) {
    case 'image':
      return '图片'
    case 'audio':
      return '音频'
    case 'video':
      return '视频'
    case 'pdf':
      return 'PDF'
    case 'text':
      return '文本'
    default:
      return '文件'
  }
}
