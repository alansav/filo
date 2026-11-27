// Tests of the find page's logic (assets/find.js), run with node --test by TestFindScript (find_test.go).
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const Find = require(path.join(__dirname, '..', 'assets', 'find.js'));

// The data as Filo writes it: tags by key and value, files newest first with their tags' positions.
const data = {
  title: 'Files',
  browse: '.index/index.html',
  tags: [
    { k: 'sender', v: 'British Gas', p: 'sender/british-gas-1.html' }, // 0
    { k: 'sender', v: 'EDF', p: 'sender/edf-2.html' }, // 1
    { k: 'type', v: 'bill', p: 'type/bill-3.html' }, // 2
    { k: 'type', v: 'letter', p: 'type/letter-4.html' }, // 3
    { k: '', v: 'urgent', p: '_/urgent-5.html' }, // 4
    { k: '', v: 'paid', p: '_/paid-6.html' }, // 5
  ],
  files: [
    { n: 'gas-oct.pdf', d: 'received/2026/10', h: './received/2026/10/gas-oct.pdf', t: '2026-10-03', g: [0, 2, 4] },
    { n: 'edf-sep.pdf', d: 'received/2026/09', h: './received/2026/09/edf-sep.pdf', t: '2026-09-12', g: [1, 2, 5] },
    { n: 'gas-sep.pdf', d: 'received/2026/09', h: './received/2026/09/gas-sep.pdf', t: '2026-09-02', g: [0, 2, 5] },
    { n: 'Welcome Letter.pdf', d: 'received/2025', h: './received/2025/Welcome%20Letter.pdf', t: '2025-01-05', g: [0, 3] },
    { n: 'notes.txt', d: '', h: './notes.txt', t: '2024-06-30', g: [] },
  ],
};
const model = Find.prepare(data);
const names = (files) => files.map((f) => f.name);
const state = (s) => Object.assign(Find.emptyState(), s);

test('matches: values of one key match either way, keys and bare tags must all match', () => {
  const cases = [
    { name: 'nothing selected: every file, newest first', s: {},
      want: ['gas-oct.pdf', 'edf-sep.pdf', 'gas-sep.pdf', 'Welcome Letter.pdf', 'notes.txt'] },
    { name: 'one tag', s: { tags: [1] }, want: ['edf-sep.pdf'] },
    { name: 'two values of one key: either', s: { tags: [0, 1] },
      want: ['gas-oct.pdf', 'edf-sep.pdf', 'gas-sep.pdf', 'Welcome Letter.pdf'] },
    { name: 'two keys: both', s: { tags: [0, 3] }, want: ['Welcome Letter.pdf'] },
    { name: 'either sender, and a bill', s: { tags: [0, 1, 2] }, want: ['gas-oct.pdf', 'edf-sep.pdf', 'gas-sep.pdf'] },
    { name: 'two bare tags: both', s: { tags: [4, 5] }, want: [] },
    { name: 'a bare tag and a key', s: { tags: [5, 0] }, want: ['gas-sep.pdf'] },
    { name: 'a directory and those under it', s: { dir: 'received/2026' },
      want: ['gas-oct.pdf', 'edf-sep.pdf', 'gas-sep.pdf'] },
    { name: 'a directory is not a prefix of a name', s: { dir: 'received/202' }, want: [] },
    { name: 'search on the name, ignoring case', s: { q: 'WELCOME' }, want: ['Welcome Letter.pdf'] },
    { name: 'search on the directory', s: { q: '2026/09' }, want: ['edf-sep.pdf', 'gas-sep.pdf'] },
    { name: 'every word must match', s: { q: 'gas  sep' }, want: ['gas-sep.pdf'] },
    { name: 'all together', s: { tags: [0], dir: 'received', q: 'gas' }, want: ['gas-oct.pdf', 'gas-sep.pdf'] },
  ];
  for (const c of cases) {
    assert.deepEqual(names(Find.matches(model, state(c.s))), c.want, c.name);
  }
});

test('facets: counts narrow, other values of a selected key stay offered, selected tags are left out', () => {
  const view = (fs) => fs.map((k) => k.key + ' ' + k.values.map((v) => v.label + '=' + v.count).join(','));
  assert.deepEqual(view(Find.facets(model, state({}))), [
    'sender sender:British Gas=3,sender:EDF=1',
    'type type:bill=3,type:letter=1',
    ' paid=2,urgent=1',
  ]);
  // sender:EDF selected: EDF is a chip; British Gas still offered (counted as if no sender were chosen); the other
  // keys counted among EDF's files.
  assert.deepEqual(view(Find.facets(model, state({ tags: [1] }))), [
    'sender sender:British Gas=3',
    'type type:bill=1',
    ' paid=1',
  ]);
  // A search narrows the counts too; a key with nothing left is left out.
  assert.deepEqual(view(Find.facets(model, state({ q: 'welcome' }))), [
    'sender sender:British Gas=1',
    'type type:letter=1',
  ]);
});

test('suggest: labels holding the text, among what is offered, most files first, at most the limit', () => {
  const labels = (vs) => vs.map((v) => v.label);
  assert.deepEqual(labels(Find.suggest(model, state({}), 'gas', 8)), ['sender:British Gas']);
  // The key is part of the label: typing a key lists its values.
  assert.deepEqual(labels(Find.suggest(model, state({}), 'SENDER', 8)), ['sender:British Gas', 'sender:EDF']);
  assert.deepEqual(labels(Find.suggest(model, state({}), 'E', 8)),
    ['sender:British Gas', 'type:bill', 'sender:EDF', 'type:letter', 'urgent']);
  assert.deepEqual(labels(Find.suggest(model, state({}), 'e', 1)), ['sender:British Gas']);
  assert.deepEqual(labels(Find.suggest(model, state({}), '  ', 8)), []);
  assert.deepEqual(labels(Find.suggest(model, state({ tags: [1] }), 'letter', 8)), [], 'EDF sent no letter');
  assert.deepEqual(labels(Find.suggest(model, state({ tags: [3] }), 'bill', 8)), ['type:bill'],
    'another value of a selected key is still offered');
});

test('subdirs: the directories directly under the chosen one holding selected files', () => {
  const view = (ds) => ds.map((d) => d.dir + '=' + d.count);
  assert.deepEqual(view(Find.subdirs(model, state({}))), ['received=4']);
  assert.deepEqual(view(Find.subdirs(model, state({ dir: 'received' }))), ['received/2025=1', 'received/2026=3']);
  assert.deepEqual(view(Find.subdirs(model, state({ dir: 'received', tags: [3] }))), ['received/2025=1']);
  assert.deepEqual(view(Find.subdirs(model, state({ dir: 'received/2026/10' }))), []);
});

test('the address: written and read back; unknown tags and directories ignored; read only as text', () => {
  const s = state({ tags: [0, 2], dir: 'received/2026', q: 'gas & co' });
  const hash = Find.formatAddress(model, s);
  assert.equal(hash, '#tag=sender%3ABritish+Gas&tag=type%3Abill&dir=received%2F2026&q=gas+%26+co');
  assert.deepEqual(Find.parseAddress(model, hash), s);
  assert.equal(Find.formatAddress(model, Find.emptyState()), '');
  assert.deepEqual(Find.parseAddress(model, ''), Find.emptyState());
  // A tag in another case is the tag; one the index lacks, or given twice, counts once or not at all.
  assert.deepEqual(Find.parseAddress(model, '#tag=TYPE:BILL&tag=type:bill&tag=nope:x').tags, [2]);
  assert.equal(Find.parseAddress(model, '#dir=elsewhere').dir, '');
  // Whatever is typed stays text.
  const hostile = Find.parseAddress(model, '#q=%3Cscript%3Ealert(1)%3C%2Fscript%3E');
  assert.equal(hostile.q, '<script>alert(1)</script>');
  assert.deepEqual(names(Find.matches(model, hostile)), []);
});

// A fake of the few DOM calls the page makes, enough to run the drawing code in Node and catch mistakes in it. What
// the page looks like is checked by hand in a browser.
function fakeDOM(hash) {
  const byId = new Map();
  function element(tag) {
    const e = {
      tagName: tag, className: '', textContent: '', hidden: false, open: false, type: '', href: '', value: '',
      children: [], listeners: new Map(), attributes: new Map(),
      appendChild(c) { this.children.push(c); return c; },
      replaceChildren() { this.children = []; },
      addEventListener(type, fn) {
        if (!this.listeners.has(type)) { this.listeners.set(type, []); }
        this.listeners.get(type).push(fn);
      },
      setAttribute(k, v) { this.attributes.set(k, v); },
      focus() { doc.activeElement = this; },
      fire(type) { for (const fn of this.listeners.get(type) || []) { fn({ preventDefault() {} }); } },
      text() { return this.textContent + this.children.map((c) => c.text()).join(' '); },
      all(pred, out = []) {
        if (pred(this)) { out.push(this); }
        for (const c of this.children) { c.all(pred, out); }
        return out;
      },
    };
    return e;
  }
  for (const id of ['q', 'suggest', 'chips', 'crumbs', 'subdirs', 'keys', 'none', 'results', 'more', 'counts',
    'find', 'data']) {
    byId.set(id, element(id === 'q' ? 'input' : 'div'));
  }
  byId.get('data').textContent = JSON.stringify(data);
  const doc = {
    activeElement: null,
    getElementById: (id) => byId.get(id),
    createElement: element,
  };
  const loc = { hash: hash, pathname: '/x/index.html', search: '', replace(h) { this.hash = h; } };
  const win = { listeners: new Map(), addEventListener(t, fn) { this.listeners.set(t, fn); } };
  const hist = { replaceState(_s, _t, url) { loc.hash = url.startsWith('#') ? url : ''; } };
  return { byId, doc, loc, win, hist };
}

test('the page: drawn from the data, and the box, a suggestion, a chip and the address work', () => {
  const fs = require('node:fs');
  const vm = require('node:vm');
  const src = fs.readFileSync(path.join(__dirname, '..', 'assets', 'find.js'), 'utf8');
  const dom = fakeDOM('#tag=type%3Abill');
  vm.runInNewContext(src, {
    document: dom.doc, location: dom.loc, window: dom.win, history: dom.hist,
    JSON, URLSearchParams, Map, Set, Array, String,
  });
  const $ = (id) => dom.byId.get(id);
  assert.equal($('find').hidden, false, 'the page is shown');
  assert.equal($('counts').textContent, '3 of 5 files', 'type:bill from the address');
  assert.equal($('results').children.length, 3);
  assert.match($('chips').text(), /type: bill/);
  // Typing in the box searches and suggests.
  $('q').focus();
  $('q').value = 'edf';
  $('q').fire('input');
  assert.equal($('counts').textContent, '1 of 5 files');
  assert.equal(dom.loc.hash, '#tag=type%3Abill&q=edf');
  const suggested = $('suggest').all((e) => e.tagName === 'button');
  assert.equal($('suggest').hidden, false);
  assert.match(suggested[0].text(), /sender:EDF/);
  // Tapping the suggestion adds the tag and clears the box.
  suggested[0].fire('click');
  assert.equal($('q').value, '');
  assert.equal($('counts').textContent, '1 of 5 files');
  assert.equal(dom.loc.hash, '#tag=type%3Abill&tag=sender%3AEDF');
  // Removing a chip; going into a directory.
  $('chips').children[0].fire('click');
  assert.equal(dom.loc.hash, '#tag=sender%3AEDF');
  $('subdirs').all((e) => e.tagName === 'button')[0].fire('click');
  assert.match(dom.loc.hash, /dir=received/);
  // The address changed by hand (back, a bookmark) is read again.
  dom.loc.hash = '#q=welcome';
  dom.win.listeners.get('hashchange')();
  assert.equal($('counts').textContent, '1 of 5 files');
  assert.equal($('q').value, 'welcome');
});
