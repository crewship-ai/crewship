/** Not served by any production route. Header CSP is mandatory: a meta policy
 * cannot enforce sandbox, and must not be the authority for document replacement.
 * self-navigation remains a browser-bound residual risk, not a zero-egress claim. */
export const artifactRuntimePreparationCSP = (studioOrigin: string) => {
  const origin = new URL(studioOrigin)
  if (origin.protocol !== 'https:' || origin.origin !== studioOrigin) throw new Error('Invalid Studio origin.')
  return `sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; media-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors ${origin.origin}`
}

/** A credentialless constant bootstrap. The response host must independently
 * validate separate registrable sites; this helper does not establish that gate.
 * Fragment nonce is correlation only, never authorization. */
export function artifactRuntimePreparationDocument(studioOrigin: string): string {
  artifactRuntimePreparationCSP(studioOrigin)
  const parentOrigin = JSON.stringify(studioOrigin).replace(/</g, '\\u003c')
  return `<!doctype html><meta charset="utf-8"><script>
(() => {
  const parentOrigin = ${parentOrigin};
  const nonce = location.hash.slice(1);
  if (!/^[a-zA-Z0-9-]{16,128}$/.test(nonce) || parent === window) return;
  let admitted = false;
  const connect = event => {
    if (admitted || event.source !== parent || event.origin !== parentOrigin || event.data?.type !== 'artifact.connect' || event.data?.nonce !== nonce || event.ports.length !== 1) return;
    admitted = true; removeEventListener('message', connect);
    const port = event.ports[0];
    let loaded = false;
    port.onmessage = event => {
      const data = event.data;
      if (loaded || data?.type !== 'load' || data.protocol !== 1 || typeof data.html !== 'string' || new TextEncoder().encode(data.html).byteLength > 10485760 || data.identity?.profile !== 'interactive-html-v1' || typeof data.identity.artifactId !== 'string' || !data.identity.artifactId || data.identity.artifactId.length > 128 || !Number.isSafeInteger(data.identity.revision) || data.identity.revision <= 0) { port.close(); return; }
      loaded = true;
      // The closure/port survive replacement. Header policy must survive too;
      // this is a browser gate, not a reason to replace CSP with document meta.
      document.open(); document.write(data.html); document.close();
      port.onmessage = event => {
        const message = event.data;
        if (message?.type === 'scrollTo' && Number.isFinite(message.x) && Number.isFinite(message.y) && message.x >= 0 && message.y >= 0 && message.x <= 1000000 && message.y <= 1000000) scrollTo(message.x, message.y);
        else port.close();
      };
      port.postMessage({type:'ready'});
      setInterval(() => port.postMessage({type:'heartbeat'}), 5000);
    };
    port.start();
  };
  addEventListener('message', connect);
  parent.postMessage({type:'artifact.ready',nonce}, parentOrigin);
})();
</script>`
}
