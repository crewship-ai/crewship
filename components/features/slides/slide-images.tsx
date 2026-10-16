"use client"

import { createContext, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react"
import type { SlideImage } from "@/lib/slides/model"
import { loadPageImage, PageImageAuthorization, PageImageCache } from "@/lib/slides/page-images"

const ImageContext = createContext<PageImageCache | undefined>(undefined)
const AuthorizationContext = createContext<{ identity: string; reloadKey: number; scope: PageImageAuthorization } | undefined>(undefined)
const noSubscription = () => () => {}
const zero = () => 0
export function SlideImagesProvider({ workspaceId, slug, pageId, revision, reloadKey = 0, denied = false, children }: { workspaceId: string; slug: string; pageId: string; revision: number; reloadKey?: number; denied?: boolean; children: ReactNode }) {
  const latestSlug = useRef(slug); latestSlug.current = slug
  const parent = useContext(AuthorizationContext)
  const identity = JSON.stringify([workspaceId, pageId])
  const authorization = useMemo(() => {
    if (parent?.identity === identity) return parent
    const scope = new PageImageAuthorization()
    if (denied) scope.deny("Page access or identity changed.")
    return { identity, reloadKey, scope }
  }, [parent, identity, reloadKey, denied])
  const source = JSON.stringify([workspaceId, pageId, revision, reloadKey, denied])
  const [current, setCurrent] = useState<{ source: string; cache: PageImageCache; scope: PageImageAuthorization }>()
  useEffect(() => {
    // Allocate the external resource per effect lifetime (including StrictMode
    // replay); never reuse a disposed cache after setup/cleanup/setup.
    const cache = new PageImageCache((image, signal) => loadPageImage(workspaceId, latestSlug.current, pageId, image, signal), undefined, undefined, authorization.scope.deny)
    if (denied) cache.dispose("Page access or identity changed.")
    const unregister = authorization.scope.register(cache)
    setCurrent({ source, cache, scope: authorization.scope })
    return () => { unregister(); cache.dispose() }
  }, [source, workspaceId, pageId, denied, authorization])
  return <AuthorizationContext.Provider value={authorization}><ImageContext.Provider value={current?.source === source && current.scope === authorization.scope ? current.cache : undefined}>{children}</ImageContext.Provider></AuthorizationContext.Provider>
}
export const useSlideImageCache = () => useContext(ImageContext)

export function NativeSlideImage({ image, width, height }: { image: SlideImage; width: number; height: number }) {
  const cache = useContext(ImageContext)
  useSyncExternalStore(cache?.subscribe ?? noSubscription, cache?.snapshot ?? zero, zero)
  const { asset_id, expected_sha256 } = image
  const key = `${asset_id}:${expected_sha256}`
  const [result, setResult] = useState<{ cache: PageImageCache; key: string; url?: string; error?: string }>()
  useEffect(() => {
    if (!cache) return
    let active = true
    let acquired: ReturnType<PageImageCache["acquire"]> | undefined
    try {
      acquired = cache.acquire({ asset_id, expected_sha256, alt: "" })
      void acquired.promise.then(url => { if (active) setResult({ cache, key, url }) }, cause => { if (active) setResult({ cache, key, error: cause instanceof Error ? cause.message : "Image unavailable." }) })
    } catch (cause) { setResult({ cache, key, error: cause instanceof Error ? cause.message : "Image unavailable." }) }
    return () => { active = false; acquired?.release() }
  }, [cache, asset_id, expected_sha256, key])
  const matches = result?.cache === cache && result?.key === key
  const url = matches && !cache?.closed ? result?.url : undefined
  if (url) return <image href={url} width={width} height={height} preserveAspectRatio="xMidYMid meet" role="img" aria-label={image.alt || "Slide image"}><title>{image.alt}</title></image>
  const reason = cache?.closed ? cache.reason ?? "Image source changed." : matches ? result?.error : undefined
  return <g role="img" aria-label={reason ? `Image unavailable: ${image.alt}` : `Loading image: ${image.alt}`}><rect width={width} height={height} fill="#eeeeee" stroke="#777777" /><foreignObject width={width} height={height}><div className="h-full overflow-hidden p-2 text-sm text-black">{reason ? `Image unavailable: ${reason}` : cache ? "Loading authorized image…" : "Image requires its saved Page context."}{image.alt && <p>{image.alt}</p>}</div></foreignObject></g>
}
