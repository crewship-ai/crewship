// Actual pinned native WASM in Chromium, synthetic delayed-host projections.
// No live data or SQL; the separately run product test proves those boundaries.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('@playwright/test');
(async () => {
  const [bindings, fixturePath] = process.argv.slice(2);
  const fixtures = JSON.parse(fs.readFileSync(fixturePath));
  const browser = await chromium.launch({ headless: true, ...(process.env.C1_CHROMIUM ? { executablePath: process.env.C1_CHROMIUM } : {}) });
  try {
    const page = await browser.newPage();
    await page.route('**/*', async route => {
      const u = new URL(route.request().url());
      assert.equal(u.origin, 'http://typing-fixture.invalid');
      if (u.pathname === '/') return route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>C1 typing seam</title>' });
      const name = path.basename(u.pathname);
      assert.ok(['harness.js', 'harness_bg.wasm'].includes(name));
      await route.fulfill({ contentType: name.endsWith('.wasm') ? 'application/wasm' : 'text/javascript', body: fs.readFileSync(path.join(bindings, name)) });
    });
    await page.goto('http://typing-fixture.invalid/');
    const checks = await page.evaluate(async fixtures => {
      const { default: init, C1Harness } = await import('/harness.js');
      await init({ module_or_path: '/harness_bg.wasm' });
      const checks = [];
      const yes = (v, name) => { if (!v) throw Error(name); checks.push(name); };
      const refused = (f, name) => { let threw = false; try { f(); } catch { threw = true; } yes(threw, name); };
      const ids = n => JSON.stringify(Array.from({ length: n }, (_, i) => `doc:g1:block:${i}`));
      const fresh = () => { const h = new C1Harness(fixtures.initial_json, ids(1)); h.enable_fluid(); return h; };
      const run = (h, id, params = {}) => h.run_registry(id, JSON.stringify(params));
      const text = h => JSON.parse(h.native_json()).body.map(p => p.text).join('\n');
      const drafts = h => JSON.parse(JSON.parse(h.drafts_json()).at(-1));
      const captured = h => JSON.parse(h.captured_json()).map(JSON.parse);
      const h = fresh(), identity = h.document_identity();
      for (const c of ['a', 'b', 'č']) run(h, 'text.insert', { text: c });
      yes(text(h) === 'abč', 'characters visible before any host result');
      yes(captured(h).length === 0, 'typing coalesces before deadline');
      h.tick_typing(0.1); yes(captured(h).length === 0, 'no premature send');
      h.tick_typing(0.12);
      yes(captured(h).length === 1 && captured(h)[0].params.text === 'abč', 'one coalesced text.insert');
      run(h, 'text.newParagraph'); run(h, 'text.insert', { text: 'Ž' });
      yes(text(h) === 'Abč\nŽ', 'Enter and typing visible while previous batch is pending');
      h.tick_typing(20); yes(captured(h).length === 1, 'at most one in flight');
      yes(drafts(h).count === 3 && drafts(h).text === 'abč\nŽ', 'all unconfirmed input retained');
      const project = (item, n) => h.project_confirmed(item.native_json, ids(n), true, JSON.stringify(item.selection));
      project(fixtures.items[2], 1);
      yes(text(h) === 'Abč\nŽ' && drafts(h).count === 2, 'confirmed first batch preserves later preview');
      h.tick_typing(21); yes(captured(h)[1].command === 'text.newParagraph', 'Enter occupies its queue position');
      project(fixtures.items[3], 2); h.tick_typing(22);
      yes(captured(h)[2].params.text === 'Ž' && captured(h)[2].target.block_id.endsWith(':1'), 'text after Enter targets confirmed new block');
      project(fixtures.items[4], 2);
      yes(text(h) === 'Abč\nŽ' && drafts(h).count === 0 && !h.pending(), 'all input drained only after exact own confirmations');
      yes(h.document_identity() === identity, 'same native document identity'); h.free();
      const timed = fresh(); run(timed, 'text.insert', { text: 'a' }); timed.tick_typing(0.12); timed.tick_typing(0.15);
      run(timed, 'text.insert', { text: 'b' });
      timed.project_confirmed(fixtures.items[0].native_json, ids(1), true, JSON.stringify(fixtures.items[0].selection));
      timed.tick_typing(0.26); yes(captured(timed).length === 1, 'next text batch has its own collection deadline after previous ACK');
      timed.tick_typing(0.28); yes(captured(timed).length === 2 && captured(timed)[1].params.text === 'b', 'next batch flushes at its bounded deadline'); timed.free();
      const ordered = fresh(); run(ordered, 'text.insert', { text: 'a' }); run(ordered, 'para.align', { value: 'center' }); run(ordered, 'text.insert', { text: 'b' });
      ordered.tick_typing(0);
      yes(captured(ordered)[0].params.text === 'a' && drafts(ordered).count === 3, 'format seals previous text and stays between text commands'); ordered.free();
      const caret = fresh(); run(caret, 'text.insert', { text: 'hello ' });
      const beforeStyle = caret.native_json();
      refused(() => run(caret, 'format.bold', { value: true }), 'nonempty caret-only formatting explicitly refused before preview');
      yes(caret.native_json() === beforeStyle && drafts(caret).count === 1, 'unsupported caret format cannot change preview or queue'); caret.free();
      const large = fresh(); refused(() => run(large, 'text.insert', { text: 'x'.repeat(65000) }), 'oversized individual input rejected before preview');
      yes(text(large) === '' && drafts(large).count === 0 && drafts(large).error.includes('NOT accepted'), 'oversized input is visibly refused without partial preview'); large.free();
      const full = fresh(); for (let i = 0; i < 256; i++) run(full, 'format.bold', { value: Boolean(i % 2) });
      const before = full.native_json(); refused(() => run(full, 'text.insert', { text: 'x' }), 'full queue explicitly rejects new input');
      yes(full.native_json() === before && drafts(full).count === 256 && drafts(full).error.includes('full'), 'full queue retains accepted input and reports capacity'); full.free();
      const unknown = fresh(); run(unknown, 'text.insert', { text: 'č' }); unknown.tick_typing(1); run(unknown, 'text.insert', { text: 'ž' }); unknown.pause_typing();
      refused(() => run(unknown, 'text.insert', { text: 'x' }), 'unknown result pauses further acceptance explicitly'); unknown.tick_typing(30);
      yes(text(unknown) === 'čž' && drafts(unknown).text === 'čž' && captured(unknown).length === 1, 'unknown retains original plus unsent text without replay'); unknown.free();
      const peer = fresh(); run(peer, 'text.insert', { text: 'č' });
      refused(() => peer.project_confirmed(fixtures.initial_json, ids(1), false, ''), 'peer projection cannot silently rebase queued typing');
      yes(text(peer) === 'č' && drafts(peer).text === 'č', 'peer conflict retains preview and draft'); peer.free();
      const moved = new C1Harness(fixtures.items[3].native_json, ids(2)); moved.enable_fluid(); moved.set_selection(1, 0, 0);
      // Preserve opaque native numeric bytes: splice the original empty
      // paragraph literally instead of round-tripping native JSON through JS.
      const raw = fixtures.items[3].native_json, end = raw.indexOf('],"comments":'), last = raw.lastIndexOf('{"kind":"para"', end);
      if (end < 0 || last < 0) throw Error('fixture layout changed');
      const inserted = raw.slice(0, last) + raw.slice(last, end) + ',' + raw.slice(last);
      moved.project_confirmed(inserted, JSON.stringify(['doc:g1:block:0', 'peer:new', 'doc:g1:block:1']), false, '');
      yes(JSON.parse(moved.selection_json()).focus.path[0] === 2, 'peer Enter remaps caret by stable identity');
      run(moved, 'text.insert', { text: 'x' }); moved.tick_typing(1);
      yes(captured(moved)[0].target.block_id === 'doc:g1:block:1', 'typing after peer Enter targets original paragraph'); moved.free();
      const reader = fresh(); reader.set_allowed(false);
      refused(() => run(reader, 'text.insert', { text: 'x' }), 'reader cannot enqueue optimistic writing');
      yes(text(reader) === '' && captured(reader).length === 0, 'reader state remains unchanged'); reader.free();
      return checks;
    }, fixtures);
    console.log(JSON.stringify({ evidence: 'actual native WASM, synthetic host projections', checks, passed: checks.length }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
