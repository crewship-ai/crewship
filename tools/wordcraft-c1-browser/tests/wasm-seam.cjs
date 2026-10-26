// Actual Chromium + pinned native WASM. Synthetic fixture driver; no SQL,
// transport permission, host Saved, keyboard/IME or real-user qualification.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('@playwright/test');
(async () => {
  const [bindings, fixtureManifest, commandFixture] = process.argv.slice(2);
  const fixtures = JSON.parse(fs.readFileSync(fixtureManifest));
  const commands = JSON.parse(fs.readFileSync(commandFixture));
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.route('**/*', async route => {
      const u = new URL(route.request().url());
      assert.equal(u.origin, 'http://c1-fixture.invalid');
      if (u.pathname === '/') return route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>C1 synthetic native seam</title>' });
      const basename = path.basename(u.pathname);
      assert.ok(['harness.js', 'harness_bg.wasm'].includes(basename));
      await route.fulfill({ contentType: basename.endsWith('.wasm') ? 'application/wasm' : 'text/javascript', body: fs.readFileSync(path.join(bindings, basename)) });
    });
    await page.goto('http://c1-fixture.invalid/');
    const result = await page.evaluate(async ({ fixtures, commands }) => {
      const { default: init, C1Harness } = await import('/harness.js');
      await init({ module_or_path: '/harness_bg.wasm' });
      const checks = [];
      const canonical = v => Array.isArray(v) ? v.map(canonical) : v && typeof v === "object" ? Object.fromEntries(Object.keys(v).sort().map(k => [k, canonical(v[k])])) : v;
      const eq = (a, b, name) => { if (JSON.stringify(canonical(a)) !== JSON.stringify(canonical(b))) throw Error(name); checks.push(name); };
      const yes = (v, name) => { if (!v) throw Error(name); checks.push(name); };
      const refused = (f, name) => { let threw = false; try { f(); } catch { threw = true; } yes(threw, name); };
      const ids = n => JSON.stringify(Array.from({length: n}, (_, i) => `doc:g1:block:${i}`));
      for (const f of fixtures) {
        let h; try { h = new C1Harness(f.raw, ids(JSON.parse(f.raw).body.length)); } catch(e) { throw Error(`constructor_${f.name}: ${e}`); }
        eq(JSON.parse(h.native_json()), JSON.parse(f.raw), `native_io_lossless_${f.name}`);
        h.free();
      }
      const initial = commands.initial_json;
      const h = new C1Harness(initial, ids(1));
      const identity = h.document_identity();
      for (const [i, item] of commands.items.entries()) {
        const beforeRaw = h.native_json();
        const before = JSON.parse(beforeRaw);
        h.run_registry(item.command, JSON.stringify(item.params));
        eq(JSON.parse(h.native_json()), before, `no_optimistic_native_${i}`);
        yes(h.pending(), `own_pending_${i}`);
        const proposal = JSON.parse(JSON.parse(h.captured_json()).at(-1));
        eq(proposal.params, item.params, `exact_params_${i}`);
        eq(proposal.target.start, item.target.start, `confirmed_utf8_offset_${i}`);
        eq(proposal.target.block_id, JSON.parse(ids(before.body.length))[item.target.block], `confirmed_colon_id_${i}`);
        refused(() => h.run_registry('text.insert', '{"text":"NOT_BUFFERED"}'), `pending_explicit_refusal_not_draft_${i}`);
        yes(h.status().includes("This input was not accepted"), `visible_pending_refusal_${i}`);
        // A peer projection, even identical native data, never resolves own pending.
        try { h.project_confirmed(beforeRaw, ids(before.body.length), false, ''); } catch(e) { throw Error(`peer_projection_${i}: ${e}`); }
        yes(h.pending(), `peer_does_not_unblock_${i}`);
        refused(() => h.project_confirmed(item.native_json, ids(item.native.body.length), true, ''), `missing_confirmed_selection_${i}`);
        yes(h.pending(), `invalid_ack_retains_pending_${i}`);
        if (i === 0) {
          const nativeBefore = h.native_json(), idsBefore = h.stable_ids_json(), selBefore = h.selection_json();
          const oversized = JSON.stringify({...item.selection, padding: 'é'.repeat(1024)});
          yes(oversized.length < 2048 && new TextEncoder().encode(oversized).length > 2048, 'multibyte_fixture_tests_bytes_not_chars');
          refused(() => h.project_confirmed(item.native_json, ids(item.native.body.length), true, oversized), 'oversized_multibyte_selection_refused');
          refused(() => h.project_confirmed(item.native_json, ids(item.native.body.length), false, JSON.stringify(item.selection)), 'peer_false_nonempty_selection_refused');
          eq(h.native_json(), nativeBefore, 'selection_input_refusal_preserves_native');
          eq(h.stable_ids_json(), idsBefore, 'selection_input_refusal_preserves_ids');
          eq(h.selection_json(), selBefore, 'selection_input_refusal_preserves_caret');
          yes(h.pending(), 'selection_input_refusal_preserves_pending');
        }
        const confirmedSelection = i === 0 ? JSON.stringify(item.selection).padEnd(2048, " ") : JSON.stringify(item.selection);
        if (i === 0) eq(new TextEncoder().encode(confirmedSelection).length, 2048, "exact_selection_byte_cap_fixture");
        try { h.project_confirmed(item.native_json, ids(item.native.body.length), true, confirmedSelection); } catch(e) { throw Error(`own_projection_${i}: ${e}`); }
        eq(JSON.parse(h.selection_json()), item.selection, `actual_engine_selection_${i}`);
        eq(JSON.parse(h.native_json()), item.native, `confirmed_native_${i}`);
        yes(!h.pending(), `own_ack_unblocks_${i}`);
        eq(h.document_identity(), identity, `native_identity_preserved_${i}`);
      }
      eq(JSON.parse(h.captured_json()).length, commands.items.length, 'pending_rejections_capture_nothing');
      for (const command of ['file.open', 'format.italic', 'edit.redo', 'list.bullet']) {
        refused(() => h.run_registry(command, '{}'), `closed_catalog_${command}`);
      }
      const denied = new C1Harness(initial, ids(1));
      denied.set_allowed(false);
      refused(() => denied.run_registry('text.insert', '{"text":"denied"}'), 'caller_gate_denial');
      yes(!denied.pending(), 'denied_no_pending');
      eq(JSON.parse(denied.captured_json()).length, 0, 'denied_no_capture');
      denied.free();
      const failed = new C1Harness(initial, ids(1));
      failed.set_capture_ok(false);
      refused(() => failed.run_registry('text.insert', '{"text":"uncertain"}'), 'capture_error_refused');
      yes(failed.pending(), 'capture_error_retains_original');
      refused(() => failed.run_registry('text.insert', '{"text":"successor"}'), 'capture_error_blocks_successor');
      eq(JSON.parse(failed.captured_json()).length, 1, 'capture_error_only_one_original');
      eq(JSON.parse(failed.native_json()), commands.initial, 'capture_error_no_native_mutation');
      failed.free();
      for (const [command, params] of [['format.bold', {}], ['format.font', {name:'Arial'}], ['format.size', {size:14}], ['para.align', {value:'center'}], ['edit.undo', {}]]) {
        const f = new C1Harness(initial, ids(1));
        f.run_registry(command, JSON.stringify(params));
        const proposal = JSON.parse(JSON.parse(f.captured_json())[0]);
        eq(proposal.command, command === 'edit.undo' ? 'participant.undo' : command, `existing_catalog_mapping_${command}`);
        eq(proposal.params, params, `existing_catalog_params_${command}`);
        eq(JSON.parse(f.native_json()), commands.initial, `format_undo_no_local_mutation_${command}`);
        f.free();
      }
      const unicode = fixtures.find(f => f.name === 'unicode');
      const u = new C1Harness(unicode.raw, ids(1));
      u.set_selection(0, 1, 1);
      refused(() => u.run_registry('text.insert', '{"text":"x"}'), 'invalid_utf8_selection_refused');
      eq(JSON.parse(u.captured_json()).length, 0, 'invalid_selection_no_capture');
      const before = u.native_json();
      const bad = JSON.parse(unicode.raw); bad.unknownField = true;
      refused(() => u.project_confirmed(JSON.stringify(bad), ids(1), false, ''), 'lossy_native_refused');
      eq(u.native_json(), before, 'invalid_projection_preserves_native');
      const e = commands.items.find(x => x.command === 'text.newParagraph');
      refused(() => u.project_confirmed(e.native_json, '["duplicate","duplicate"]', false, ''), 'duplicate_ids_refused');
      u.stop();
      refused(() => u.run_registry('text.insert', '{"text":"x"}'), 'stopped_refused');
      h.free(); u.free();
      return { checks, count: checks.length, scope: 'actual pinned native registry/codec WASM in Chromium; no keyboard input, pending draft, IME, SQL, Saved or live UI qualification' };
    }, { fixtures: fixtures.fixtures.map(f => ({ name: f.name, raw: fs.readFileSync(f.path, 'utf8') })), commands });
    console.log(JSON.stringify(result));
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
