import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { readFile } from 'node:fs/promises';

async function fixture(file, respond) {
  const classes = new Map(), timers = new Map(), storage = new Map(), calls = [];
  let tick = 0;
  class LitElement {
    isConnected = true;
    renderRoot = { querySelector: () => null };
    updateComplete = Promise.resolve();
    connectedCallback() {}
    disconnectedCallback() { this.isConnected = false; }
  }
  const html = (strings, ...values) => ({ strings, values });
  const context = vm.createContext({
    window: {}, location: { hash: '' },
    sessionStorage: { getItem: k => storage.get(k) ?? null, setItem: (k,v) => storage.set(k,v), removeItem: k => storage.delete(k) },
    localStorage: { getItem: () => null },
    customElements: { define: (name, cls) => classes.set(name, cls) },
    setTimeout: fn => { timers.set(++tick, fn); return tick; }, clearTimeout: id => timers.delete(id),
    clearInterval() {}, console,
  });
  const modules = {
    lit: new vm.SyntheticModule(['LitElement','html'], function() { this.setExport('LitElement', LitElement); this.setExport('html', html); }, { context }),
    'hls.js': new vm.SyntheticModule(['default'], function() { this.setExport('default', { isSupported: () => false }); }, { context }),
    '../lib/api.js': new vm.SyntheticModule(['api'], function() { this.setExport('api', async (path, opts={}) => { calls.push({path, opts}); return respond(path,opts); }); }, { context }),
  };
  const source = await readFile(new URL('../components/'+file, import.meta.url), 'utf8');
  const mod = new vm.SourceTextModule(source, {context});
  await mod.link(name => modules[name]); await mod.evaluate();
  const instance = new ([...classes.values()][0])();
  return { instance, calls, storage, timers };
}

// Read event bindings from Lit templates without duplicating the component's logic.
function handler(template, label) {
  if (Array.isArray(template)) {
    for (const value of template) { const found = handler(value,label); if (found) return found; }
  }
  if (!template?.strings) return null;
  if ((template.strings.join('') + template.values.filter(v => typeof v === 'string').join('')).includes(label)) {
    for (let i=0;i<template.values.length;i++) {
      if (template.strings[i].endsWith('@click=') && typeof template.values[i] === 'function') return template.values[i];
    }
  }
  for (const value of template.values) { const found = handler(value,label); if (found) return found; }
  return null;
}

test('refresh restores server-owned session and stops through cookie auth without storing secrets', async () => {
  const session = { id:'owned', status:'active', ingest:{ rtmp_url:'rtmp://test', stream_key:'secret' } };
  const f = await fixture('cc-playground.js', (path,opts) => {
    if (path === '/v1/capabilities') return {data:[]};
    if (path.startsWith('/portal/live-streams?')) return {items:[{id:'owned',status:'active'}]};
    if (path === '/portal/live-streams/owned') return opts.method === 'DELETE' ? {ok:true} : {session};
    throw Error('Unexpected '+path);
  });
  f.storage.set('lvp_live_selection','another-owner');
  await f.instance.connectedCallback();
  assert.equal(f.instance.liveSession.id,'owned');
  assert.equal(f.instance.liveSession.ingest.stream_key,'secret');
  assert.equal(f.storage.get('lvp_live_selection'),'owned');
  assert.ok(![...f.storage.values()].includes('secret'));
  const stop = handler(f.instance.render(),'Stop stream');
  assert.ok(stop); await stop.call(f.instance);
  assert.equal(f.instance.liveSession.status,'ending');
  const deletion = f.calls.find(c => c.opts.method === 'DELETE');
  assert.equal(deletion.path,'/portal/live-streams/owned');
  assert.equal(deletion.opts.headers.Authorization,undefined);
  f.instance.disconnectedCallback();
  assert.equal(f.timers.size,0);
});

test('history stop is retryable and remains tracked through refresh', async () => {
  let attempts=0;
  const f = await fixture('cc-live-streams.js', (path,opts) => {
    if (opts.method === 'DELETE') { if (++attempts === 1) throw Error('offline'); return {ok:true}; }
    return {items:[{id:'owned',status:'active',created_at:'2026-09-24T00:00:00Z'}]};
  });
  await f.instance.connectedCallback();
  // Find the Stop button binding in the rendered row.
  function findStop(t) {
    if (Array.isArray(t)) { for (const v of t) { const f=findStop(v); if(f)return f; } }
    if (!t?.strings) return null;
    for (let i=0;i<t.values.length;i++) if (t.strings[i].endsWith('@click=') && t.strings[i+1]?.includes('>\n')) {
      if (t.strings.join('').includes('ghost danger')) return t.values[i];
    }
    for (const v of t.values) { const f=findStop(v); if(f)return f; }
  }
  const stop = findStop(f.instance.render()); assert.ok(stop);
  await stop(); assert.match(f.instance.error,/retry/i); assert.equal(f.instance.stopping.size,0);
  await stop(); assert.equal(f.instance.rows[0].status,'ending'); assert.equal(f.instance.stopping.size,1);
  f.instance.disconnectedCallback(); assert.equal(f.timers.size,0);
});
