// This fixed bootstrap receives code only from its configured Studio parent.
// The Page SDK gets a one-way snapshot port, never a session token or API URL.
let booted = false;
window.addEventListener('message', event => {
  if (booted || event.source !== parent || event.origin !== expectedParent || event.data?.type !== 'crewship.pages.boot/v1' || event.ports.length !== 1) return;
  const artifact = event.data.artifact;
  if (artifact?.format !== 'crewship-page-preview/v1' || typeof artifact.javascript !== 'string' || typeof artifact.css !== 'string' || new TextEncoder().encode(artifact.javascript + artifact.css).length > 2097152) return;
  booted = true;
  const port = event.ports[0];
  window.__crewshipPagesPort = port;
  // Wait for initial data and two paint opportunities, not the empty document's
  // load event. This works for existing artifacts and Pages without SDK hooks.
  const firstSnapshot = message => {
    if (message.data?.type !== 'crewship.pages.snapshot/v1') return;
    port.removeEventListener('message', firstSnapshot);
    requestAnimationFrame(() => requestAnimationFrame(() => {
      port.postMessage({ type: 'crewship.pages.rendered/v1' });
    }));
  };
  port.addEventListener('message', firstSnapshot);
  port.start();
  const style = document.createElement('style');
  style.textContent = artifact.css;
  document.head.append(style);
  const script = document.createElement('script');
  script.nonce = bootNonce;
  script.textContent = artifact.javascript;
  document.body.append(script);
});
