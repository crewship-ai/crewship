import DOMPurify from "dompurify"
import { staticCSS } from "./artifact-raster"
import type { ArtifactFile } from "@/hooks/use-artifact-project"

/** Preview source is inert. Only the host-owned selection bridge may execute. */
export function artifactProjectDocument(files: ArtifactFile[], path: string, token: string, selecting: boolean): string {
 const file = files.find(file => file.path === path)
 if (!file || file.content.length > 2 * 1024 * 1024 || new TextEncoder().encode(file.content).length > 2 * 1024 * 1024 || !DOMPurify.isSupported) return "<!doctype html><p>Preview unavailable.</p>"
 const purifier = DOMPurify(window)
 purifier.addHook("uponSanitizeAttribute", (_node, attr) => { if (attr.attrName === "style" && !staticCSS(attr.attrValue)) attr.keepAttr = false })
 purifier.addHook("uponSanitizeElement", node => { if (node.nodeName === "STYLE" && !staticCSS(node.textContent ?? "")) node.textContent = "" })
 const fragment = purifier.sanitize(file.content, { RETURN_DOM_FRAGMENT: true, FORCE_BODY: true, USE_PROFILES: { html: true }, ADD_TAGS: ["style"], FORBID_TAGS: ["script", "iframe", "frame", "object", "embed", "meta", "base", "link", "form", "input", "textarea", "select", "svg", "math", "video", "audio", "source", "template"], FORBID_ATTR: ["src", "srcset", "action", "formaction", "target", "download", "ping", "background", "poster"], ALLOW_DATA_ATTR: false })
 // Only links to HTML files in this exact manifest can navigate the preview.
 for (const link of fragment.querySelectorAll("[href]")) {
  const href = link.getAttribute("href") ?? ""
  link.removeAttribute("href")
  let target: string
  try { target = new URL(href, `https://artifact.invalid/${path}`).pathname.slice(1) } catch { continue }
  if (link.tagName === "A" && !/^[a-z][a-z\d+.-]*:/i.test(href) && !href.startsWith("//") && files.some(file => file.path === target && file.media_type === "text/html")) link.setAttribute("data-artifact-screen", target)
 }
 const holder = document.createElement("div"); holder.append(fragment)
 const projectCSS = files.filter(file => file.media_type === "text/css").map(file => file.content).join("\n")
 const safeProjectCSS = staticCSS(projectCSS) ? projectCSS : ""
 const nonce = Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, "0")).join("")
 const payload = JSON.stringify({ token, selecting }).replaceAll("<", "\\u003c")
 const bridge = `const config=${payload};document.addEventListener('click',event=>{event.preventDefault();const element=event.target instanceof Element?event.target:null;if(!element)return;if(!config.selecting){const link=element.closest('[data-artifact-screen]');if(link)parent.postMessage({token:config.token,screen:link.getAttribute('data-artifact-screen')},'*');return;}const parts=[];let current=element;while(current&&current!==document.body){const parent=current.parentElement;if(!parent)break;parts.unshift(current.tagName.toLowerCase()+':nth-child('+(Array.from(parent.children).indexOf(current)+1)+')');current=parent;}parent.postMessage({token:config.token,selector:element.id?'#'+CSS.escape(element.id):'body > '+parts.join(' > '),quote:(element.textContent||'').trim().slice(0,500)},'*');},true);`
 const policy = `default-src 'none'; script-src 'nonce-${nonce}'; style-src 'unsafe-inline'; img-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'`
 return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"><style>body{font-family:system-ui,sans-serif;margin:24px;overflow-wrap:anywhere}a,button{cursor:pointer}${selecting ? "body *:hover{outline:2px solid #2563eb;cursor:crosshair}" : ""}</style></head><body>${safeProjectCSS ? `<style>${safeProjectCSS.replaceAll("<", "\\3c ")}</style>` : ""}${holder.innerHTML}<script nonce="${nonce}">${bridge}</script></body></html>`
}
