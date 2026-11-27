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
  }
})();
