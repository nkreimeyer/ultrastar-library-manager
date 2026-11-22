// Checks of the fixes in static/app.js (phases, history, stop, done titles), run by TestAppJSFixes: node appjs_check_fixes.js <app.js>.
// app.js runs in a vm context with a small fake DOM and a fetch the checks answer.
"use strict";

const fs = require("fs");
const vm = require("vm");

class Node {}
class Element extends Node {
  constructor(tag) {
    super();
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.style = { setProperty() {} };
    this.classList = { toggle() {}, add() {}, remove() {} };
    this.className = "";
    this.disabled = false;
    this._text = "";
  }
  append(...kids) {
    for (const k of kids) this.children.push(k instanceof Node ? k : String(k));
  }
  replaceChildren(...kids) {
    this.children = [];
    this.append(...kids);
  }
  setAttribute(k, v) {
    this.attrs[k] = v;
  }
  addEventListener(type, fn) {
    this.listeners[type] = fn;
  }
  querySelector() {
    return null;
  }
  querySelectorAll() {
    return [];
  }
  set textContent(v) {
    this._text = String(v);
    this.children = [];
  }
  get textContent() {
    return this._text + this.children.map((k) => (k instanceof Node ? k.textContent : k)).join("");
  }
  // all returns this element and every element below it.
  all() {
    return [this, ...this.children.filter((k) => k instanceof Node).flatMap((k) => k.all())];
  }
}

const byId = {};
const document = {
  createElement: (tag) => new Element(tag),
  getElementById: (id) => byId[id] || null,
  querySelectorAll: () => [],
};
// The elements of index.html (next to app.js).
const indexHTML = fs.readFileSync(require("path").join(require("path").dirname(process.argv[2]), "index.html"), "utf8");
for (const [, tag, id] of indexHTML.matchAll(/<([a-z]+)[^>]*\sid="([^"]+)"/g)) byId[id] = new Element(tag);

// fetch answers: the checks put a handler per path; unknown paths never answer.
const answers = {};
const requests = [];
function fetch(path, init) {
  requests.push({ path, body: init?.body ? JSON.parse(init.body) : null });
  const answer = answers[path];
  if (!answer) return new Promise(() => {});
  return answer(init?.body ? JSON.parse(init.body) : null);
}
const reply = (status, data) => Promise.resolve({ ok: status < 300, status, statusText: String(status), text: async () => JSON.stringify(data) });

const ctx = {
  console, document, fetch, Node, Intl, JSON, Promise, Date, Math, Set, Map, Object, Array, String, Number, Error,
  setTimeout, clearTimeout, queueMicrotask,
  localStorage: { getItem: () => null, setItem() {} },
  window: { scrollTo() {} },
  EventSource: class { addEventListener() {} },
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], "utf8"), ctx, { filename: "app.js" });

let failures = 0;
function check(ok, msg) {
  if (!ok) {
    failures++;
    console.log("FAIL: " + msg);
  }
}
// run evaluates code in the page's scope (its top-level const and let).
const run = (code) => vm.runInContext(code, ctx);
const tick = () => new Promise((r) => setTimeout(r, 0));

async function main() {
  // A check plans the moves: its progress line must not say songs are moved.
  check(run(`phaseText({ dryRun: true, phase: "move" })`) !== "Moving songs", "a dry run's move phase reads " + run(`phaseText({ dryRun: true, phase: "move" })`));
  check(run(`phaseText({ dryRun: false, phase: "move" })`) === "Moving songs", "a real run's move phase");
  check(run(`phaseText({ dryRun: true, phase: "inspect" })`) === "Checking songs", "a dry run's inspect phase");

  // Setting aside broken songs leaves the ones with an encoding problem in place.
  check(run(`checkStays({ status: "broken", findings: [{ code: "encoding" }, { code: "no-audio" }] })`) !== "", "a broken song with an encoding problem is counted as moved");
  check(run(`checkStays({ status: "broken", findings: [{ code: "no-audio" }] })`) === "", "a plain broken song stays");
  check(run(`checkStays({ status: "suspicious", findings: [{ code: "encoding" }] })`) === "", "a song with warnings only is no broken song");
  check(run(`checkStays({ status: "broken", notMoved: "why", findings: [] })`) === "why", "notMoved of an apply's report");

  // Paths are joined with the separator of the server's system.
  run(`S.sys = { os: "windows" }`);
  check(run(`joinPath("D:\\\\Karaoke\\\\out", "invalid")`) === "D:\\Karaoke\\out\\invalid", "a Windows path: " + run(`joinPath("D:\\\\Karaoke\\\\out", "invalid")`));
  run(`S.sys = { os: "linux" }`);
  check(run(`joinPath("/k/out", "invalid")`) === "/k/out/invalid", "a Unix path");

  // A failed run that moved some songs was not stopped by the user.
  const failed = { stopped: false, job: { state: "failed", stats: { moved: 3 } } };
  ctx.failedDone = failed;
  check(run(`doneTitle(failedDone)`) === "Not everything could be done", "a failed run's title: " + run(`doneTitle(failedDone)`));
  ctx.okDone = { stopped: false, job: { state: "done", stats: { moved: 3 } } };
  check(run(`doneTitle(okDone)`) === "Done", "a finished run's title: " + run(`doneTitle(okDone)`));
  const failedText = run(`movedText(failedDone)`);
  check(!failedText.includes("you stopped") && failedText.includes("Undo puts them back"), "a failed run's text: " + failedText);
  ctx.stoppedDone = { stopped: true, job: { state: "cancelled", stats: { moved: 3 } } };
  check(run(`doneTitle(stoppedDone)`) === "Stopped part-way" && run(`movedText(stoppedDone)`).includes("you stopped"), "a stopped run");
  answers["/api/remove/analysis?id=r1"] = () => reply(200, {});
  run(`S.handled = new Set(); S.rem = { config: { library: "/lib", outDir: "/out" } }`);
  await run(`onJobEnd({ id: "j1", kind: "remove-apply", state: "failed", error: "journal", stats: { moved: 2 }, result: { analysis: "r1", library: "/lib", outDir: "/out" } })`);
  check(run(`S.view`) === "done" && run(`S.done.stopped`) === false, "a failed remove apply is shown as stopped");
  run(`S.handled = new Set()`);
  await run(`onJobEnd({ id: "j2", kind: "check-apply", state: "failed", error: "x", stats: { moved: 2 }, result: { analysis: "c1", library: "/lib", outDir: "/out" } })`);
  check(run(`S.done.stopped`) === false, "a failed check apply is shown as stopped");
  run(`S.handled = new Set()`);
  await run(`onJobEnd({ id: "j3", kind: "organize-apply", state: "failed", error: "x", stats: { moved: 2 }, result: { analysis: "o1", library: "/lib", outDir: "/out" } })`);
  check(run(`S.done.stopped`) === false, "a failed organize apply is shown as stopped");

  // The report viewer shows sizes like the rest of the page.
  run(`S.reports = { config: { outDir: "/out" }, files: [{ name: "organize_plan.txt", size: 1500000, modified: "2026-11-22T10:00:00Z" }, { name: "empty.txt", size: 0, modified: "2026-11-22T10:00:00Z" }] }`);
  const reports = run(`viewReports()`).textContent;
  check(reports.includes("1.5 MB") && reports.includes("0 bytes"), "report sizes: " + reports);

  // A history answer for a library the page left is dropped, and the undo button undoes
  // a run of the list shown.
  let releaseA;
  answers["/api/organize/history"] = (body) => body.library === "/A"
    ? new Promise((r) => { releaseA = () => r({ ok: true, status: 200, text: async () => JSON.stringify({ outDir: "/A-out", runs: [{ id: "a1", moves: 1, next: true }] }) }); })
    : reply(200, { outDir: "/B-out", runs: [{ id: "b1", moves: 1, next: true }] });
  run(`validateSeq = 1`);
  const histA = run(`loadHistory(1, "/A", "/A-out")`);
  run(`validateSeq = 2`);
  await run(`loadHistory(2, "/B", "/B-out")`);
  releaseA();
  await histA;
  check(run(`S.history.runs[0].id`) === "b1", "the late history of an earlier library replaced the current one: " + run(`S.history.runs[0].id`));
  run(`S.resolved = null; S.history = { library: "/A", outDir: "/A-out", runs: [{ id: "a1", moves: 1, next: true }] }`);
  const undoBtn = run(`historyList()`).all().find((e) => e.tagName === "button");
  answers["/api/organize/undo"] = () => reply(202, { id: "u1", state: "running" });
  requests.length = 0;
  undoBtn.listeners.click();
  await tick();
  const undoReq = requests.find((r) => r.path === "/api/organize/undo");
  check(undoReq && undoReq.body.library === "/A" && undoReq.body.out === "/A-out", "undo of the list's run: " + JSON.stringify(undoReq));

  // Once the page resolves another library, the list of the old one goes, also while
  // the new one's history is still on its way.
  answers["/api/organize/validate"] = () => reply(200, { library: "/C", outDir: "/C-out" });
  answers["/api/organize/history"] = () => new Promise(() => {});
  run(`S.task = "organize"; S.f.library = "/C"; S.history = { library: "/A", outDir: "/A-out", runs: [{ id: "a1", moves: 1, next: true }] }`);
  await run(`validate()`);
  check(run(`S.history`) === null, "the list of the earlier library stays: " + JSON.stringify(run(`S.history`)));

  // A stop that does not go through can be tried again.
  byId.stop = new Element("button");
  byId["stop-hint"] = new Element("span");
  answers["/api/job/cancel"] = () => reply(403, { error: "not signed in" });
  await run(`stopJob()`);
  check(!byId.stop.disabled && byId.stop.textContent === "Stop", "the stop button after a failed stop: " + byId.stop.disabled + " " + byId.stop.textContent);
  check(byId["stop-hint"].textContent.includes("not signed in"), "the stop hint after a failed stop: " + byId["stop-hint"].textContent);

  if (failures) process.exit(1);
  console.log("ok");
}

main().catch((e) => {
  console.log("FAIL: " + (e.stack || e));
  process.exit(1);
});
