// Constant preparation-only bootstrap; host injects only the validated Studio origin.
(() => {
  const parentOrigin = window.__CREWSHIP_ARTIFACT_PARENT_ORIGIN__;
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
