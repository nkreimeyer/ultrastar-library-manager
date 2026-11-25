// Golden checks of static/app.js, run by TestAppJSGolden: node appjs_golden.js <app.js> [--update] [--area <name>].
// Each case loads app.js into a fresh vm context with a fake DOM, drives it (state, clicks,
// fetch answers) and returns a value: a snapshot of what the page built, or the state it
// left. The values are compared with appjs_golden/<area>.json; --update writes them. The
// cases are in appjs_golden/<area>.cases.js. They pin what the page does today, so a
// refactor of app.js must leave every value as it is.
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const appPath = process.argv[2];
const update = process.argv.includes("--update");
const areaArg = process.argv.indexOf("--area");
const onlyArea = areaArg > 0 ? process.argv[areaArg + 1] : null;
const goldenDir = path.join(__dirname, "appjs_golden");
const appSrc = fs.readFileSync(appPath, "utf8");
const indexHTML = fs.readFileSync(path.join(path.dirname(appPath), "index.html"), "utf8");

// ---------------------------------------------------------------- fake DOM

class Node {}
class Element extends Node {
  constructor(tag) {
    super();
    this.tagName = tag;
    this.attrs = {};
    this.children = [];
    this.parent = null;
    this.listeners = {};
    this.className = "";
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.value = "";
    this.open = false;
    this.returnValue = "";
    this.onclose = null;
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.dataset = {};
    const el = this;
    this.style = { props: {}, setProperty(k, v) { this.props[k] = v; } };
    this.classList = {
      toggle(c, on) {
        const set = new Set(el.className.split(" ").filter(Boolean));
        if (on === undefined ? !set.has(c) : on) set.add(c);
        else set.delete(c);
        el.className = [...set].join(" ");
      },
      add(c) { this.toggle(c, true); },
      remove(c) { this.toggle(c, false); },
      contains: (c) => el.className.split(" ").includes(c),
    };
  }
  get id() { return this.attrs.id || ""; }
  set id(v) { this.attrs.id = String(v); }
  get title() { return this.attrs.title || ""; }
  set title(v) { this.attrs.title = String(v); }
  get childNodes() { return this.children; }
  get firstChild() { return this.children[0] || null; }
  append(...kids) {
    for (const k of kids) {
      if (k instanceof Node) {
        k.remove();
        k.parent = this;
        this.children.push(k);
      } else this.children.push(String(k));
    }
  }
  appendChild(k) {
    this.append(k);
    return k;
  }
  replaceChildren(...kids) {
    for (const k of this.children) if (k instanceof Node) k.parent = null;
    this.children = [];
    this.append(...kids);
  }
  remove() {
    if (!this.parent) return;
    const p = this.parent;
    p.children = p.children.filter((k) => k !== this);
    this.parent = null;
  }
  setAttribute(k, v) {
    this.attrs[k] = String(v);
  }
  getAttribute(k) {
    return k in this.attrs ? this.attrs[k] : null;
  }
  removeAttribute(k) {
    delete this.attrs[k];
  }
  addEventListener(type, fn) {
    (this.listeners[type] ||= []).push(fn);
  }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn);
  }
  set textContent(v) {
    this.replaceChildren();
    if (String(v) !== "") this.children.push(String(v));
  }
  get textContent() {
    return this.children.map((k) => (k instanceof Node ? k.textContent : k)).join("");
  }
  // all returns this element and every element below it, in document order.
  all() {
    return [this, ...this.children.filter((k) => k instanceof Node).flatMap((k) => k.all())];
  }
  matches(sel) {
    // Only "tag", ".class", "tag.class", with descendant (" ") and child (">") combinators.
    const parts = sel.trim().replace(/\s*>\s*/g, " > ").split(/\s+/);
    return matchesFrom(this, parts);
  }
  querySelectorAll(sel) {
    return this.all().slice(1).filter((e) => e.matches(sel));
  }
  querySelector(sel) {
    return this.querySelectorAll(sel)[0] || null;
  }
  showModal() {
    this.open = true;
  }
  close() {
    this.open = false;
    if (this.onclose) this.onclose();
    this.dispatch("close");
  }
  focus() {}
  click() {
    this.dispatch("click");
  }
  dispatch(type, extra) {
    const ev = { type, target: this, currentTarget: this, preventDefault() {}, stopPropagation() {}, ...extra };
    const out = [];
    for (const fn of this.listeners[type] || []) out.push(fn(ev));
    return out;
  }
}

function matchesPart(el, part) {
  const m = /^([a-z0-9]*)((?:\.[\w-]+)*)$/i.exec(part);
  if (!m) return false;
  if (m[1] && m[1] !== el.tagName) return false;
  const own = el.className.split(" ");
  return m[2].split(".").filter(Boolean).every((c) => own.includes(c));
}

// matchesFrom reports whether el matches the last part of parts and its ancestors the rest.
function matchesFrom(el, parts) {
  if (!matchesPart(el, parts[parts.length - 1])) return false;
  const rest = parts.slice(0, -1);
  if (!rest.length) return true;
  if (rest[rest.length - 1] === ">") return !!el.parent && matchesFrom(el.parent, rest.slice(0, -1));
  for (let anc = el.parent; anc; anc = anc.parent) if (matchesFrom(anc, rest)) return true;
  return false;
}

// snap turns an element into plain data: tag, attributes and the properties app.js sets,
// listener types and children (text as strings, "" included), in order.
function snap(n) {
  if (!(n instanceof Element)) return n === null || n === undefined ? n : String(n);
  const o = { t: n.tagName };
  const a = {};
  for (const k of Object.keys(n.attrs).sort()) a[k] = n.attrs[k];
  if (n.className) a.class = n.className;
  for (const p of ["hidden", "disabled", "checked", "open"]) if (n[p]) a["." + p] = true;
  if (n.value !== "") a[".value"] = n.value;
  if (Object.keys(n.style.props).length) a[".style"] = { ...n.style.props };
  if (Object.keys(n.dataset).length) a[".dataset"] = { ...n.dataset };
  if (Object.keys(a).length) o.a = a;
  const on = Object.keys(n.listeners).filter((k) => n.listeners[k].length).sort();
  if (on.length) o.on = on;
  if (n.children.length) o.c = n.children.map(snap);
  return o;
}

// ---------------------------------------------------------------- one page per case

const FIXED_NOW = Date.UTC(2026, 10, 25, 10, 0, 0);
const RealDate = Date;
class FixedDate extends RealDate {
  constructor(...args) {
    if (args.length) super(...args);
    else super(FIXED_NOW);
  }
  static now() {
    return FIXED_NOW;
  }
}
// Number formatting with one locale, whatever the machine's is.
const FixedIntl = Object.create(Intl);
FixedIntl.NumberFormat = function (locale, opts) {
  return new Intl.NumberFormat("en-US", opts);
};
FixedIntl.DateTimeFormat = function (locale, opts) {
  return new Intl.DateTimeFormat("en-US", { timeZone: "UTC", ...opts });
};

function newPage({ storage = {} } = {}) {
  const body = new Element("body");
  for (const [, tag, attrs] of indexHTML.matchAll(/<([a-z]+)((?:\s+[a-z-]+(?:="[^"]*")?)*)\s*\/?>/g)) {
    const id = /\sid="([^"]+)"/.exec(attrs);
    if (!id) continue;
    const el = new Element(tag);
    el.id = id[1];
    const cls = /\sclass="([^"]+)"/.exec(attrs);
    if (cls) el.className = cls[1];
    body.append(el);
  }
  const document = {
    body,
    createElement: (tag) => new Element(tag),
    getElementById: (id) => body.all().find((e) => e.attrs.id === id) || null,
    querySelectorAll: (sel) => body.querySelectorAll(sel),
    querySelector: (sel) => body.querySelector(sel),
  };
  const store = new Map(Object.entries(storage));
  const localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
  const answers = {};
  const requests = [];
  function fetch(url, init) {
    const body = init?.body ? JSON.parse(init.body) : null;
    requests.push({ method: init?.method || "GET", url, body });
    const p = String(url).split("?")[0];
    const answer = answers[url] || answers[p];
    if (!answer) return new Promise(() => {});
    return Promise.resolve(answer(body, url));
  }
  const reply = (status, data) => ({ ok: status < 300, status, statusText: String(status), text: async () => JSON.stringify(data) });
  const scrolls = [];
  const timers = [];
  const window = {
    scrollY: 0,
    listeners: {},
    scrollTo(x, y) {
      scrolls.push([x, y]);
    },
    addEventListener(type, fn) {
      (this.listeners[type] ||= []).push(fn);
    },
    location: { href: "http://127.0.0.1/", reload() {} },
  };
  const ctx = {
    console, document, window, localStorage, fetch, Node, Intl: FixedIntl, Date: FixedDate,
    JSON, Promise, Math, Set, Map, Object, Array, String, Number, Error, RegExp, encodeURIComponent, decodeURIComponent,
    queueMicrotask,
    setTimeout: (fn, ms) => timers.push({ fn, ms }),
    clearTimeout() {},
    setInterval: () => 0,
    clearInterval() {},
    EventSource: class { addEventListener() {} close() {} },
    URL, URLSearchParams, Blob: class {},
  };
  vm.createContext(ctx);
  vm.runInContext(appSrc, ctx, { filename: "app.js" });
  const run = (code) => vm.runInContext(code, ctx);
  const tick = () => new Promise((r) => setImmediate(r));
  const env = {
    ctx, run, document, window, answers, requests, reply, scrolls, timers, store, snap,
    // set assigns values from the test into the page's scope (S.done = ..., etc.).
    set(name, value) {
      ctx.__v = value;
      run(`${name} = __v`);
      delete ctx.__v;
    },
    // flush lets pending promises (fetch answers, awaits) run.
    async flush(n = 10) {
      for (let i = 0; i < n; i++) await tick();
    },
    byId: (id) => document.getElementById(id),
    find(root, pred) {
      return (root || body).all().find(pred) || null;
    },
    // button finds the element whose text is text (a button, a link button).
    button(root, text) {
      return (root || body).all().find((e) => e.tagName === "button" && e.textContent === text) || null;
    },
    buttons: (root) => (root || body).all().filter((e) => e.tagName === "button").map((e) => e.textContent),
    // click calls the element's click listeners and waits for what they start.
    async click(el) {
      if (!el) throw new Error("click: no element");
      el.dispatch("click");
      await env.flush();
    },
    // confirm answers the open confirm dialog.
    async confirm(ok) {
      const dlg = document.getElementById("confirm");
      dlg.returnValue = ok ? "ok" : "";
      dlg.close();
      await env.flush();
    },
    // state is plain data of S (Sets as sorted arrays), for the cases that check what a
    // function left behind.
    state(keys) {
      const out = {};
      for (const k of keys) {
        const v = run(`S[${JSON.stringify(k)}]`);
        out[k] = v instanceof ctx.Set || (v && v.constructor && v.constructor.name === "Set") ? [...v].sort() : JSON.parse(JSON.stringify(v ?? null));
      }
      return out;
    },
    app: () => document.getElementById("app"),
  };
  return env;
}

// ---------------------------------------------------------------- golden compare

function firstDiff(a, b, at = "") {
  if (JSON.stringify(a) === JSON.stringify(b)) return null;
  if (a && b && typeof a === "object" && typeof b === "object") {
    for (const k of new Set([...Object.keys(a), ...Object.keys(b)])) {
      const d = firstDiff(a[k], b[k], `${at}/${k}`);
      if (d) return d;
    }
  }
  return { at: at || "/", want: a, got: b };
}
const short = (v) => {
  const s = JSON.stringify(v);
  return s === undefined ? "undefined" : s.length > 300 ? s.slice(0, 300) + "…" : s;
};

async function main() {
  const areas = fs.readdirSync(goldenDir).filter((f) => f.endsWith(".cases.js")).map((f) => f.slice(0, -".cases.js".length)).sort();
  let failures = 0;
  let count = 0;
  for (const area of areas) {
    if (onlyArea && area !== onlyArea) continue;
    const cases = require(path.join(goldenDir, `${area}.cases.js`));
    const file = path.join(goldenDir, `${area}.json`);
    const want = fs.existsSync(file) ? JSON.parse(fs.readFileSync(file, "utf8")) : {};
    const got = {};
    const names = new Set();
    for (const c of cases) {
      if (names.has(c.name)) throw new Error(`${area}: case "${c.name}" twice`);
      names.add(c.name);
      count++;
      let value;
      try {
        value = await c.run(newPage(c.page));
        value = value === undefined ? null : JSON.parse(JSON.stringify(value));
      } catch (e) {
        value = { threw: String(e && e.message) };
        if (!c.throws) {
          failures++;
          console.log(`FAIL ${area}/${c.name}: threw ${e && e.stack}`);
          continue;
        }
      }
      got[c.name] = value;
      if (update) continue;
      if (!(c.name in want)) {
        failures++;
        console.log(`FAIL ${area}/${c.name}: no golden value (run with --update)`);
        continue;
      }
      const d = firstDiff(want[c.name], value);
      if (d) {
        failures++;
        console.log(`FAIL ${area}/${c.name}: differs at ${d.at}\n  want ${short(d.want)}\n  got  ${short(d.got)}`);
      }
    }
    if (update) {
      // One line per case, so a diff names the cases that changed.
      const lines = Object.entries(got).map(([k, v]) => `${JSON.stringify(k)}: ${JSON.stringify(v)}`);
      fs.writeFileSync(file, `{\n${lines.join(",\n")}\n}\n`);
      console.log(`wrote ${path.relative(process.cwd(), file)}: ${Object.keys(got).length} cases`);
    } else {
      for (const k of Object.keys(want)) {
        if (!names.has(k)) {
          failures++;
          console.log(`FAIL ${area}/${k}: golden value without a case`);
        }
      }
    }
  }
  if (failures) {
    console.log(`${failures} of ${count} golden cases failed`);
    process.exit(1);
  }
  console.log(update ? "updated" : "ok");
}

main().catch((e) => {
  console.log("FAIL: " + (e.stack || e));
  process.exit(1);
});
