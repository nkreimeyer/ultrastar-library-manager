// Tests the pure logic of static/app.js with node built-ins only. app.js is loaded as it
// is into a vm context with stubs for the browser; nothing in it is changed or exported
// by hand: a line appended to the source copy hands the internals to this script.
// Usage: node appjs_check.js <path to app.js>
"use strict";
const vm = require("node:vm");
const fs = require("node:fs");
const assert = require("node:assert/strict");

const src = fs.readFileSync(process.argv[2], "utf8");

// A stub element: any property is a function or element, so top-level wiring does not crash.
function stubEl() {
  const target = function () {};
  const own = { classList: { add() {}, remove() {}, toggle() {} }, style: { setProperty() {} }, dataset: {} };
  return new Proxy(target, {
    get: (t, k) => (k in own ? own[k] : k === Symbol.toPrimitive ? () => "" : k === "then" ? undefined : stubEl()),
    set: () => true,
    apply: () => stubEl(),
  });
}
const store = new Map();
let storeThrows = false;
const localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => {
    if (storeThrows) throw new Error("quota");
    store.set(k, String(v));
  },
};
class Node {}
const ctx = vm.createContext({
  document: { getElementById: () => stubEl(), querySelectorAll: () => [], createElement: () => stubEl() },
  window: { scrollTo() {} },
  localStorage,
  Node,
  fetch: () => Promise.reject(new Error("no network in tests")),
  EventSource: class { addEventListener() {} close() {} },
  queueMicrotask,
  setTimeout: () => 0,
  clearTimeout() {},
  setInterval: () => 0,
  clearInterval() {},
  console,
});
vm.runInContext(
  src + "\n;globalThis.__app = { S, organizeRequest, tidyRequest, etaText, bytesText, loadForm, recentLibraries, rememberLibrary, emptyForm, TIDY_DEFAULTS, RECENT_MAX };",
  ctx,
  { filename: "app.js" },
);
const A = ctx.__app;
const S = A.S;
// Objects of the vm context have another prototype: compare through JSON, which is also
// what the browser sends.
const J = (x) => JSON.parse(JSON.stringify(x));

let clock = 0;
ctx.__clock = () => clock;
vm.runInContext("Date.now = () => __clock()", ctx);

const failures = [];
function test(name, fn) {
  try {
    fn();
  } catch (e) {
    failures.push(`${name}: ${e.message}`);
  }
}
function setForm(task, mode, f) {
  S.task = task;
  S.mode = mode;
  S.f = { ...A.emptyForm(), ...f };
  S.sys = null;
}

test("bytesText", () => {
  const b = A.bytesText;
  assert.equal(b(0), "0 bytes");
  assert.equal(b(999), "999 bytes");
  assert.equal(b(1000), "1.0 KB");
  assert.equal(b(1500), "1.5 KB");
  assert.equal(b(9949), "9.9 KB");
  assert.equal(b(10000), "10 KB");
  assert.equal(b(12345678), "12 MB");
  assert.equal(b(2.5e9), "2.5 GB");
  // Rounding crosses the one-digit limit: 9.99 KB prints as "10 KB", not "10.0 KB".
  assert.equal(b(9990), "10 KB");
  // Rounding crosses the unit: 999.999 KB prints as "1.0 MB", not "1000 KB".
  assert.equal(b(999999), "1.0 MB");
  assert.equal(b(999499), "999 KB");
  // TB is the last unit: it does not grow into a larger one.
  assert.equal(b(1e15), "1000 TB");
  assert.equal(b(5e12), "5.0 TB");
  assert.equal(b(1), "1 byte");
  assert.equal(b(2), "2 bytes");
});

test("tidyRequest", () => {
  setForm("dedupe", "easy", {});
  S.f.tidy = { ...A.TIDY_DEFAULTS, audio: true, video: true };
  let t = J(A.tidyRequest());
  assert.equal(t.audio, true);
  assert.equal(t.video, true);
  S.sys = { toolsOK: false };
  t = J(A.tidyRequest());
  assert.equal(t.audio, false);
  assert.equal(t.video, false);
  assert.equal(t.utf8, true, "the other options stay");
  assert.equal(S.f.tidy.audio, true, "the form is not changed");
  S.sys = { toolsOK: true };
  assert.equal(J(A.tidyRequest()).audio, true);
  // Without system info (not loaded yet) the tools are assumed to be there.
  S.sys = null;
  assert.equal(J(A.tidyRequest()).video, true);
});

test("organizeRequest dedupe/organize", () => {
  setForm("dedupe", "easy", {
    library: "  /lib  ",
    out: "/out",
    tiers: [["/a"], ["/b"]],
    priority: true,
    ignoreThe: true,
    deep: true,
  });
  let r = A.organizeRequest(false);
  assert.ok(!("tidy" in J(r)), "no tidy without withTidy");
  assert.equal(r.tidy, undefined);
  assert.deepEqual(J(r), { mode: "dedupe", tiers: [], library: "/lib", out: "", priority: false, ignoreThe: false, deep: false });
  r = A.organizeRequest(true);
  assert.equal(r.tidy.utf8, true);
  assert.equal(J(r).tidy.audio, true);
  assert.equal(J(r).tiers.length, 0, "a dedupe has no tiers");

  S.mode = "advanced";
  r = J(A.organizeRequest(true));
  assert.deepEqual([r.out, r.priority, r.ignoreThe, r.deep], ["/out", true, true, true]);

  // tidy only for dedupe
  S.task = "organize";
  r = A.organizeRequest(true);
  assert.equal(r.tidy, undefined);
  assert.equal(r.mode, "organize");
  S.task = "add";
  assert.equal(A.organizeRequest(true).mode, "add");

  // tiers: paths trimmed, empty paths and empty tiers dropped
  S.task = "organize";
  S.f.tiers = [[" /a ", "", "  "], [""], ["  "], ["/b", " /c"]];
  assert.deepEqual(J(A.organizeRequest(false)).tiers, [["/a"], ["/b", "/c"]]);
  S.f.tiers = [[""]];
  assert.deepEqual(J(A.organizeRequest(false)).tiers, []);
  // advanced values are trimmed too, and false when not set
  S.f.out = "  /o  ";
  S.f.priority = false;
  r = J(A.organizeRequest(false));
  assert.deepEqual([r.out, r.priority], ["/o", false]);
});

test("organizeRequest other tasks", () => {
  setForm("remove", "easy", { library: " /lib ", out: "/out", patterns: "  ABBA \n", deep: true });
  assert.deepEqual(J(A.organizeRequest(true)), { library: "/lib", out: "", patterns: "  ABBA \n" }, "patterns are sent as typed");
  S.mode = "advanced";
  assert.deepEqual(J(A.organizeRequest(false)), { library: "/lib", out: "/out", patterns: "  ABBA \n" });

  setForm("check", "easy", { library: "/lib", out: "/out", deep: true });
  assert.deepEqual(J(A.organizeRequest(false)), { library: "/lib", out: "", deep: false });
  S.mode = "advanced";
  assert.deepEqual(J(A.organizeRequest(false)), { library: "/lib", out: "/out", deep: true });
  S.f.deep = false;
  assert.equal(A.organizeRequest(false).deep, false);

  for (const task of ["restore", "reports", "originals", "overview", "export"]) {
    setForm(task, "easy", { library: " /lib ", out: "/out" });
    assert.deepEqual(J(A.organizeRequest(true)), { library: "/lib", out: "" }, task);
    S.mode = "advanced";
    assert.deepEqual(J(A.organizeRequest(true)), { library: "/lib", out: "/out" }, task);
  }
});

test("etaText", () => {
  const job = (o) => ({ id: "j1", phase: "p", done: 0, total: 100, ...o });
  const at = (ms, j) => {
    clock = ms;
    return A.etaText(j);
  };
  S.eta = undefined;
  assert.equal(at(0, job({ done: 0 })), "", "first call starts the clock");
  assert.equal(at(1000, job({ done: 3 })), "", "under 5 s");
  assert.equal(at(6000, job({ done: 2 })), "", "done went backwards: restarts the clock");
  assert.equal(at(12000, job({ done: 4 })), "", "fewer than 3 done since the restart");
  S.eta = undefined;
  at(0, job({ done: 0 }));
  assert.equal(at(6000, job({ done: 3 })), "about 3 min left", "6 s per 3 -> 194 s");
  assert.equal(at(10000, job({ done: 50 })), "less than a minute left");
  assert.equal(at(10000, job({ done: 100 })), "", "finished");
  assert.equal(at(10000, job({ total: 0, done: 50 })), "", "unknown total");
  // hours
  S.eta = undefined;
  at(0, job({ total: 3000 }));
  assert.equal(at(6000, job({ total: 3000, done: 3 })), "about 1 h 40 min left");
  // The minutes are rounded before the hours are cut off: 7198 s left is "2 h 0 min".
  S.eta = undefined;
  at(0, job({ total: 3602 }));
  assert.equal(at(6000, job({ total: 3602, done: 3 })), "about 2 h 0 min left");
  // 3599 s left is "1 h 0 min", not "60 min".
  S.eta = undefined;
  at(0, job({ total: 1802 }));
  assert.equal(at(6000, job({ total: 1802, done: 3 })), "about 1 h 0 min left");

  // reset on job change: the pace of the old job does not count
  S.eta = undefined;
  at(0, job({}));
  assert.equal(at(6000, job({ done: 3 })), "about 3 min left");
  assert.equal(at(7000, job({ id: "j2", done: 10 })), "", "new job restarts");
  assert.equal(at(13000, job({ id: "j2", done: 13 })), "about 3 min left", "10 -> 13 in 6 s");
  // reset on phase change
  assert.equal(at(14000, job({ id: "j2", phase: "q", done: 20 })), "", "new phase restarts");
  assert.equal(at(20000, job({ id: "j2", phase: "q", done: 23 })), "about 3 min left");
  // done going backwards restarts
  assert.equal(at(21000, job({ id: "j2", phase: "q", done: 5 })), "");
  assert.equal(at(27000, job({ id: "j2", phase: "q", done: 8 })), "about 3 min left");
});

test("loadForm", () => {
  store.clear();
  let f = A.loadForm();
  assert.deepEqual(J(f), J(A.emptyForm()));
  assert.deepEqual(J(f.tiers), [[""]]);
  // saved values win, the tidy options are merged over the defaults
  store.set("lm-form", JSON.stringify({ library: "/x", tiers: [["/a"]], tidy: { video: true, audio: false } }));
  f = J(A.loadForm());
  assert.equal(f.library, "/x");
  assert.deepEqual(f.tiers, [["/a"]]);
  assert.equal(f.out, "", "missing keys get defaults");
  assert.equal(f.tidy.video, true);
  assert.equal(f.tidy.audio, false);
  assert.equal(f.tidy.utf8, true, "a missing tidy option gets its default");
  // corrupt or unusable storage falls back to an empty form
  for (const bad of ["{not json", "null"]) {
    store.set("lm-form", bad);
    assert.deepEqual(J(A.loadForm()), J(A.emptyForm()), bad);
  }
  // tidy: null is tolerated
  store.set("lm-form", JSON.stringify({ library: "/y", tidy: null }));
  f = J(A.loadForm());
  assert.equal(f.library, "/y");
  assert.deepEqual(f.tidy, J(A.TIDY_DEFAULTS));
  // the defaults are copied, not shared
  store.clear();
  A.loadForm().tidy.utf8 = false;
  assert.equal(A.TIDY_DEFAULTS.utf8, true);
});

test("recentLibraries / rememberLibrary", () => {
  store.clear();
  assert.deepEqual(J(A.recentLibraries()), []);
  store.set("lm-recent", "{broken");
  assert.deepEqual(J(A.recentLibraries()), []);
  store.set("lm-recent", JSON.stringify({ a: 1 }));
  assert.deepEqual(J(A.recentLibraries()), [], "not an array");
  store.set("lm-recent", JSON.stringify("text"));
  assert.deepEqual(J(A.recentLibraries()), []);
  store.set("lm-recent", JSON.stringify(["/a", 3, "", null, "/b"]));
  assert.deepEqual(J(A.recentLibraries()), ["/a", "/b"], "non-strings and empties dropped");

  // a corrupt list is replaced on the next remember
  store.set("lm-recent", "{broken");
  A.rememberLibrary("/new");
  assert.deepEqual(J(A.recentLibraries()), ["/new"]);
  A.rememberLibrary("  /two  ");
  assert.deepEqual(J(A.recentLibraries()), ["/two", "/new"], "trimmed, newest first");
  A.rememberLibrary("/new");
  assert.deepEqual(J(A.recentLibraries()), ["/new", "/two"], "a duplicate moves to the front");
  A.rememberLibrary("   ");
  A.rememberLibrary(undefined);
  assert.deepEqual(J(A.recentLibraries()), ["/new", "/two"], "empty ignored");
  // capped at RECENT_MAX
  store.clear();
  assert.equal(A.RECENT_MAX, 6);
  for (let i = 1; i <= 9; i++) A.rememberLibrary(`/l${i}`);
  assert.deepEqual(J(A.recentLibraries()), ["/l9", "/l8", "/l7", "/l6", "/l5", "/l4"]);
  // blocked storage is swallowed
  storeThrows = true;
  A.rememberLibrary("/blocked");
  storeThrows = false;
  assert.equal(A.recentLibraries()[0], "/l9");
});

if (failures.length) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("ok");
