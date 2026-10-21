let snapshot;
const $ = id => document.getElementById(id);
async function load() {
  snapshot = await (await fetch(`/${$('candidate').value}`)).json();
  const target = snapshot.deck.slides[0].elements[0]; $('text').value = target.text; $('x').value = target.x; $('y').value = target.y;
  $('slides').replaceChildren(...snapshot.previews.map((source, index) => { const frame = document.createElement('iframe'); frame.sandbox = ''; frame.title = snapshot.deck.slides[index].id; frame.srcdoc = source; return frame; }));
  $('status').textContent = `${$('candidate').value} Version ${snapshot.deck.version}`;
}
$('candidate').addEventListener('change', load);
$('save').addEventListener('click', async () => {
  const response = await fetch(`/${$('candidate').value}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ slideID: snapshot.deck.slides[0].id, elementID: snapshot.deck.slides[0].elements[0].id, ifVersion: snapshot.deck.version, ifHash: snapshot.targetHash, changes: { text: $('text').value, x: Number($('x').value), y: Number($('y').value) } }) });
  if (!response.ok) { $('status').textContent = `Rejected: ${(await response.json()).error}`; return; }
  await load();
});
await load();
