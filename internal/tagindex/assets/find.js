// Filo tag index: the find page's script (docs/design.md, Tag index pages, "The find page"). It reads the page's
// data (written by Filo as JSON) and never makes a request. Names and tags are untrusted: the page is built with
// createElement and textContent only, and links are used exactly as Filo wrote them.
(function () {
  'use strict';

  // ---- The logic: plain functions over the data, tested with node --test (testdata/find_test.js). ----

  // prepare turns the page's data into the model the other functions use: each file with its name and directory in
  // lower case for the search, each tag with its key, value, label (key:value, or the value of a bare tag) and its
  // label in lower case for the suggestions and the address.
  function prepare(data) {
    const tags = data.tags.map(function (t, i) {
      const label = t.k === '' ? t.v : t.k + ':' + t.v;
      return { id: i, key: t.k, value: t.v, label: label, lower: label.toLowerCase(), page: t.p };
    });
    const files = data.files.map(function (f, i) {
      return {
        id: i, name: f.n, dir: f.d, href: f.h, date: f.t, tags: f.g,
        lowerName: f.n.toLowerCase(), lowerDir: f.d.toLowerCase(),
      };
    });
    return { title: data.title, browse: data.browse, tags: tags, files: files };
  }

  // emptyState is no tag, no directory and no search.
  function emptyState() {
    return { tags: [], dir: '', q: '' };
  }

  // groups are the selected tags as the conditions a file must meet: one group per key, whose values match either
  // way, and one group per bare tag.
  function groups(model, state) {
    const byKey = new Map();
    const out = [];
    for (const id of state.tags) {
      const t = model.tags[id];
      if (t.key === '') {
        out.push({ key: null, ids: [id] });
        continue;
      }
      let g = byKey.get(t.key);
      if (!g) {
        g = { key: t.key, ids: [] };
        byKey.set(t.key, g);
        out.push(g);
      }
      g.ids.push(id);
    }
    return out;
  }

  function inDir(file, dir) {
    return dir === '' || file.dir === dir || file.dir.startsWith(dir + '/');
  }

  // words are the search's words, in lower case: a file matches when each is in its name or directory.
  function words(q) {
    return q.toLowerCase().split(/\s+/).filter(function (w) { return w !== ''; });
  }

  function searched(file, ws) {
    for (const w of ws) {
      if (!file.lowerName.includes(w) && !file.lowerDir.includes(w)) {
        return false;
      }
    }
    return true;
  }

  // meets reports whether file meets every group but skip (a key, to count that key's values as if none of them
  // were selected; undefined to skip none).
  function meets(file, gs, skip) {
    for (const g of gs) {
      if (skip !== undefined && g.key === skip) {
        continue;
      }
      if (!g.ids.some(function (id) { return file.tags.includes(id); })) {
        return false;
      }
    }
    return true;
  }

  // matches are the files the state selects, in the data's order (newest first).
  function matches(model, state) {
    const gs = groups(model, state);
    const ws = words(state.q);
    return model.files.filter(function (f) {
      return inDir(f, state.dir) && searched(f, ws) && meets(f, gs);
    });
  }

  // facets are the tags to offer, by key (the bare tags last, key ''): each tag found among the files the state
  // would select were its own key's values not selected (so another value of a selected key can still be added),
  // with how many of those files have it; most files first, then by label. Selected tags are left out: they are
  // chips. A key with no tag to offer is left out.
  function facets(model, state) {
    const gs = groups(model, state);
    const ws = words(state.q);
    const base = model.files.filter(function (f) { return inDir(f, state.dir) && searched(f, ws); });
    const counts = new Map();
    for (const f of base) {
      const own = new Map(); // key -> whether the file meets the groups skipping that key
      for (const id of f.tags) {
        const t = model.tags[id];
        const skip = t.key === '' ? undefined : t.key;
        const k = t.key === '' ? '' : t.key;
        if (!own.has(k)) {
          own.set(k, meets(f, gs, skip));
        }
        if (own.get(k)) {
          counts.set(id, (counts.get(id) || 0) + 1);
        }
      }
    }
    const byKey = new Map();
    for (const [id, n] of counts) {
      if (state.tags.includes(id)) {
        continue;
      }
      const t = model.tags[id];
      if (!byKey.has(t.key)) {
        byKey.set(t.key, []);
      }
      byKey.get(t.key).push({ id: id, label: t.label, value: t.value, count: n });
    }
    const keys = Array.from(byKey.keys()).sort(function (a, b) {
      if (a === '' || b === '') {
        return a === '' ? 1 : -1;
      }
      return a < b ? -1 : a > b ? 1 : 0;
    });
    return keys.map(function (k) {
      const values = byKey.get(k).sort(function (a, b) {
        return b.count - a.count || (a.label < b.label ? -1 : a.label > b.label ? 1 : 0);
      });
      return { key: k, values: values };
    });
  }

  // suggest are the tags to offer for text typed in the box: those whose label holds it (ignoring case), among the
  // facets, most files first, at most limit.
  function suggest(model, state, text, limit) {
    const w = text.trim().toLowerCase();
    if (w === '') {
      return [];
    }
    const out = [];
    for (const k of facets(model, state)) {
      for (const v of k.values) {
        if (model.tags[v.id].lower.includes(w)) {
          out.push(v);
        }
      }
    }
    out.sort(function (a, b) {
      return b.count - a.count || (a.label < b.label ? -1 : a.label > b.label ? 1 : 0);
    });
    return out.slice(0, limit);
  }

  // subdirs are the directories directly under the state's directory holding files the tags and search select,
  // with how many, by name.
  function subdirs(model, state) {
    const gs = groups(model, state);
    const ws = words(state.q);
    const prefix = state.dir === '' ? '' : state.dir + '/';
    const counts = new Map();
    for (const f of model.files) {
      if (f.dir === state.dir || !f.dir.startsWith(prefix) || f.dir === '' || !searched(f, ws) || !meets(f, gs)) {
        continue;
      }
      const name = f.dir.slice(prefix.length).split('/')[0];
      counts.set(name, (counts.get(name) || 0) + 1);
    }
    return Array.from(counts.keys()).sort().map(function (name) {
      return { name: name, dir: prefix + name, count: counts.get(name) };
    });
  }

  // parseAddress reads the state from the page address's fragment (#tag=type%3Abill&dir=received&q=gas). It is only
  // compared with the data: a tag the index does not have (ignoring case) is ignored, and so is a directory no file
  // is in.
  function parseAddress(model, hash) {
    const state = emptyState();
    const params = new URLSearchParams(hash.replace(/^#/, ''));
    for (const label of params.getAll('tag')) {
      const lower = label.toLowerCase();
      const t = model.tags.find(function (x) { return x.lower === lower; });
      if (t && !state.tags.includes(t.id)) {
        state.tags.push(t.id);
      }
    }
    const dir = params.get('dir') || '';
    if (dir !== '' && model.files.some(function (f) { return inDir(f, dir); })) {
      state.dir = dir;
    }
    state.q = params.get('q') || '';
    return state;
  }

  // formatAddress is the fragment for a state ("" for the empty state).
  function formatAddress(model, state) {
    const params = new URLSearchParams();
    for (const id of state.tags) {
      params.append('tag', model.tags[id].label);
    }
    if (state.dir !== '') {
      params.set('dir', state.dir);
    }
    if (state.q !== '') {
      params.set('q', state.q);
    }
    const s = params.toString();
    return s === '' ? '' : '#' + s;
  }

  const Find = {
    prepare: prepare, emptyState: emptyState, matches: matches, facets: facets, suggest: suggest,
    subdirs: subdirs, parseAddress: parseAddress, formatAddress: formatAddress,
  };

  if (typeof module === 'object' && module.exports) {
    module.exports = Find;
    return;
  }

  // ---- The page: built from the logic's answers with createElement and textContent only. ----

  const PAGE = 200; // results shown at a time

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) {
      e.className = cls;
    }
    if (text !== undefined) {
      e.textContent = text;
    }
    return e;
  }

  function plural(n, one, many) {
    return n === 1 ? '1 ' + one : n + ' ' + many;
  }

  function start() {
    const model = prepare(JSON.parse(document.getElementById('data').textContent));
    const $ = function (id) { return document.getElementById(id); };
    const box = $('q');
    let state = parseAddress(model, location.hash);
    let shown = PAGE;
    let written = location.hash;
    const open = new Set(); // keys whose values are shown
    box.value = state.q;

    function update(change) {
      change();
      shown = PAGE;
      const hash = formatAddress(model, state);
      if (hash !== written) {
        written = hash;
        try {
          history.replaceState(null, '', hash === '' ? location.pathname + location.search : hash);
        } catch (e) {
          location.replace(hash === '' ? '#' : hash);
        }
      }
      draw();
    }

    function addTag(id) {
      update(function () {
        if (!state.tags.includes(id)) {
          state.tags = state.tags.concat([id]);
        }
      });
    }

    function tagButton(id, cls, label, count) {
      const b = el('button', cls);
      b.type = 'button';
      b.appendChild(el('span', 'name', label));
      if (count !== undefined) {
        b.appendChild(el('span', 'count', String(count)));
      }
      b.addEventListener('click', function () { addTag(id); });
      return b;
    }

    function drawSuggest() {
      const list = $('suggest');
      list.replaceChildren();
      const found = document.activeElement === box ? suggest(model, state, box.value, 8) : [];
      for (const v of found) {
        const li = el('li');
        const b = tagButton(v.id, '', v.label, plural(v.count, 'file', 'files'));
        b.addEventListener('mousedown', function (e) { e.preventDefault(); }); // keep the box focused
        b.addEventListener('click', function () {
          box.value = '';
          update(function () { state.q = ''; });
          box.focus();
        });
        li.appendChild(b);
        list.appendChild(li);
      }
      list.hidden = found.length === 0;
    }

    function drawChips() {
      const chips = $('chips');
      chips.replaceChildren();
      for (const id of state.tags) {
        const t = model.tags[id];
        const b = el('button', 'chip', t.key === '' ? t.value : t.key + ': ' + t.value);
        b.type = 'button';
        b.setAttribute('aria-label', 'Remove ' + t.label);
        b.addEventListener('click', function () {
          update(function () { state.tags = state.tags.filter(function (x) { return x !== id; }); });
        });
        chips.appendChild(b);
      }
      if (state.dir !== '') {
        const b = el('button', 'chip', 'in ' + state.dir);
        b.type = 'button';
        b.setAttribute('aria-label', 'Remove the directory ' + state.dir);
        b.addEventListener('click', function () { update(function () { state.dir = ''; }); });
        chips.appendChild(b);
      }
    }

    function drawDirs() {
      const crumbs = $('crumbs');
      crumbs.replaceChildren();
      const parts = state.dir === '' ? [] : state.dir.split('/');
      const go = function (dir) { return function () { update(function () { state.dir = dir; }); }; };
      const top = el('button', '', model.title);
      top.type = 'button';
      top.addEventListener('click', go(''));
      crumbs.appendChild(top);
      parts.forEach(function (p, i) {
        crumbs.appendChild(el('span', 'sep', '›'));
        const b = el('button', '', p);
        b.type = 'button';
        b.addEventListener('click', go(parts.slice(0, i + 1).join('/')));
        crumbs.appendChild(b);
      });
      const list = $('subdirs');
      list.replaceChildren();
      for (const d of subdirs(model, state)) {
        const li = el('li');
        const b = el('button');
        b.type = 'button';
        b.appendChild(el('span', 'name', d.name + '/'));
        b.appendChild(el('span', 'count', plural(d.count, 'file', 'files')));
        b.addEventListener('click', go(d.dir));
        li.appendChild(b);
        list.appendChild(li);
      }
    }

    function drawKeys() {
      const keys = $('keys');
      keys.replaceChildren();
      for (const k of facets(model, state)) {
        const d = el('details');
        d.open = open.has(k.key);
        d.addEventListener('toggle', function () {
          if (d.open) {
            open.add(k.key);
          } else {
            open.delete(k.key);
          }
        });
        const s = el('summary', '', k.key === '' ? 'other' : k.key);
        s.appendChild(el('span', 'count', plural(k.values.length, k.key === '' ? 'tag' : 'value',
          k.key === '' ? 'tags' : 'values')));
        d.appendChild(s);
        const ul = el('ul', 'rows');
        for (const v of k.values) {
          const li = el('li');
          li.appendChild(tagButton(v.id, '', k.key === '' ? v.label : v.value, plural(v.count, 'file', 'files')));
          ul.appendChild(li);
        }
        d.appendChild(ul);
        keys.appendChild(d);
      }
    }

    function fileRow(f) {
      const li = el('li');
      const a = el('a', 'file', f.name);
      a.href = f.href; // worked out by Filo: relative and URL-encoded, used as given
      li.appendChild(a);
      const meta = el('div', 'meta');
      if (f.dir !== '') {
        meta.appendChild(el('span', 'dir', f.dir));
      }
      meta.appendChild(el('span', 'date', f.date));
      li.appendChild(meta);
      const others = f.tags.filter(function (id) { return !state.tags.includes(id); });
      if (others.length > 0) {
        const tagsDiv = el('div', 'tags');
        for (const id of others) {
          tagsDiv.appendChild(tagButton(id, 'tag', model.tags[id].label));
        }
        li.appendChild(tagsDiv);
      }
      return li;
    }

    function drawResults() {
      const found = matches(model, state);
      const total = model.files.length;
      $('counts').textContent = found.length === total ? plural(total, 'file', 'files') :
        found.length + ' of ' + plural(total, 'file', 'files');
      const list = $('results');
      list.replaceChildren();
      for (const f of found.slice(0, shown)) {
        list.appendChild(fileRow(f));
      }
      $('none').hidden = found.length > 0;
      const more = $('more');
      more.hidden = found.length <= shown;
      more.textContent = 'Show more (' + (found.length - shown) + ' left)';
    }

    function draw() {
      drawSuggest();
      drawChips();
      drawDirs();
      drawKeys();
      drawResults();
    }

    box.addEventListener('input', function () { update(function () { state.q = box.value; }); });
    box.addEventListener('focus', drawSuggest);
    box.addEventListener('blur', drawSuggest);
    $('more').addEventListener('click', function () {
      shown += PAGE;
      drawResults();
    });
    window.addEventListener('hashchange', function () {
      if (location.hash === written || (location.hash === '#' && written === '')) {
        return;
      }
      written = location.hash;
      state = parseAddress(model, location.hash);
      box.value = state.q;
      shown = PAGE;
      draw();
    });
    $('find').hidden = false;
    draw();
  }

  start();
})();
