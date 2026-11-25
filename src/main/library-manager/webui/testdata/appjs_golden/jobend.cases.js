// Golden cases of onJobEnd: what the page does when the job it waits for ends, for every
// job kind and outcome. Each case sets the state a page could be in, calls onJobEnd with a
// finished job, answers (or rejects, or never answers) the analysis GET and records the
// state left behind, the requests and scrolls made after the call, and the page drawn.
"use strict";

const LIB = "/music/lib";
const OUT = "/music/out";
const CFG = { library: LIB, outDir: OUT };
const OLD = { library: "/old/lib", outDir: "/old/out" };

const KEYS = ["view", "notice", "done", "org", "chk", "rem", "open", "rmKeep", "waitingFor", "handled", "overview", "exported"];

// Analyses as the server sends them, small enough to read.
const orgAnalysis = (id, extra = {}) => ({
  id,
  mode: "dedupe",
  config: { library: `${LIB}-${id}`, outDir: `${OUT}-${id}` },
  songs: [{ ref: "a", rel: "A/Abba - SOS", artist: "Abba", title: "SOS", action: "inplace", other: -1, quality: { video: true, videoHeight: 720 } }],
  summary: { toLibrary: 0, duplicates: 0, invalid: 0, inPlace: 1 },
  notes: [],
  similar: [],
  joined: [],
  items: [],
  ...extra,
});
const chkAnalysis = (id) => ({
  id,
  config: { library: `${LIB}-${id}`, outDir: `${OUT}-${id}` },
  report: { checked: 2, songs: [{ path: "A/Bad", status: "broken", findings: [{ code: "no-audio", severity: "error", message: "no audio" }] }], notes: [] },
});
const remAnalysis = (id) => ({
  id,
  config: { library: `${LIB}-${id}`, outDir: `${OUT}-${id}` },
  report: { summary: { songs: 3 }, removed: [{ song: "A/Abba - SOS", name: "Abba - SOS", pattern: "Abba" }], unmatched: [], leftInPlace: [] },
});
const tidyAnalysis = (id) => orgAnalysis(id, {
  tidy: { options: { utf8: true, fileTags: true, fillTags: true, leftovers: true, folders: true, audio: false, video: false }, textFiles: ["A/x.txt"], leftovers: [], brokenFolders: [] },
});

const folders = { library: LIB, outDir: OUT };
const job = (kind, state, extra = {}) => ({ id: `job-${kind}-${state}`, kind, state, error: `${state} error`, details: ["d1", "d2"], stats: {}, ...extra });

// The state a page has before: some of every kind, so resets and keeps both show.
function setBefore(env, before) {
  for (const [k, v] of Object.entries(before || {})) {
    if (k === "open" || k === "rmKeep" || k === "handled") env.run(`S.${k} = new Set(${JSON.stringify(v)})`);
    else env.set(`S.${k}`, v);
  }
}

// page sums up what the page shows: the heading, the notices and the buttons.
function page(env) {
  const app = env.app();
  const h1 = env.find(app, (e) => e.tagName === "h1");
  return {
    h1: h1 ? h1.textContent : null,
    notices: app.querySelectorAll(".notice").map((n) => n.textContent),
    buttons: env.buttons(app),
  };
}

// end runs onJobEnd(j) with the GET answers given ({ url: reply | "reject" | deferred })
// and returns what it left.
async function end(env, j, { before, answers = {}, mode, full = false, again = null } = {}) {
  if (mode) env.set("S.mode", mode);
  setBefore(env, before);
  for (const [url, a] of Object.entries(answers)) {
    if (a === "reject") env.answers[url] = () => env.reply(500, { error: `GET ${url} failed` });
    else if (typeof a === "function") env.answers[url] = a;
    else env.answers[url] = () => env.reply(200, a);
  }
  const base = env.requests.length;
  env.app().replaceChildren();
  env.ctx.__j = j;
  env.run("globalThis.__settled = false; globalThis.__rejected = null; onJobEnd(__j).then(() => { globalThis.__settled = true; }, (e) => { globalThis.__rejected = String(e && e.message); })");
  await env.flush();
  if (again) await again(env);
  const out = {
    state: env.state(KEYS),
    settled: env.run("__settled"),
    rejected: env.run("__rejected"),
    requests: env.requests.slice(base).map((r) => `${r.method} ${r.url}`),
    scrolls: env.scrolls,
    page: page(env),
  };
  if (full) out.snap = env.snap(env.app());
  return out;
}

const cases = [];
const add = (name, fn) => cases.push({ name, run: fn });

// ---------------------------------------------------------------- organize-analyse / tidy-analyse

const others = { rem: remAnalysis("r0"), chk: chkAnalysis("c0"), open: ["x"], rmKeep: ["k"], notice: { kind: "", title: "earlier notice" } };

add("organize-analyse done: loads S.org, resets rem/chk/open, keeps rmKeep and notice", (env) =>
  end(env, job("organize-analyse", "done", { result: { analysis: "a1" } }), { before: others, answers: { "/api/organize/analysis?id=a1": orgAnalysis("a1") }, full: true }));
add("organize-analyse done: analysis id is URL-encoded", (env) =>
  end(env, job("organize-analyse", "done", { result: { analysis: "a b/&?" } }), { answers: { "/api/organize/analysis?id=a%20b%2F%26%3F": orgAnalysis("x") } }));
add("organize-analyse done: get rejects -> setup with the error, rem/chk already reset", (env) =>
  end(env, job("organize-analyse", "done", { result: { analysis: "a1" } }), { before: { ...others, org: orgAnalysis("old") }, answers: { "/api/organize/analysis?id=a1": "reject" } }));
add("organize-analyse done: get never answered -> guard set, rem/chk reset, nothing drawn", (env) =>
  end(env, job("organize-analyse", "done", { result: { analysis: "a1" } }), { before: { ...others, waitingFor: "job-organize-analyse-done", view: "running" } }));
add("organize-analyse cancelled: setup, stopped notice, rem/chk reset", (env) =>
  end(env, job("organize-analyse", "cancelled"), { before: others, full: true }));
add("organize-analyse failed: setup, failed notice with text and details", (env) =>
  end(env, job("organize-analyse", "failed"), { before: others }));
add("organize-analyse failed advanced: same notice", (env) =>
  end(env, job("organize-analyse", "failed"), { mode: "advanced" }));
add("organize-analyse with an unexpected state: treated as failed", (env) =>
  end(env, job("organize-analyse", "odd")));
add("tidy-analyse done: review of a tidy analysis", (env) =>
  end(env, job("tidy-analyse", "done", { result: { analysis: "t1" } }), { before: others, answers: { "/api/organize/analysis?id=t1": tidyAnalysis("t1") } }));
add("tidy-analyse failed: options view", (env) =>
  end(env, job("tidy-analyse", "failed"), { before: { ...others, f: { library: LIB, tidy: { utf8: true, fileTags: true, fillTags: true, leftovers: true, folders: true, broken: "aside", audio: false, video: false, dedupe: true } } } }));
add("tidy-analyse cancelled: options view, stopped notice", (env) =>
  end(env, job("tidy-analyse", "cancelled")));

// ---------------------------------------------------------------- organize-apply / tidy-apply

const doneResult = (extra = {}) => ({ result: { analysis: "a1", ...folders, ...extra }, stats: { moved: 3, failed: 0 } });

add("organize-apply done with result folders and S.org: no GET, cfg from the result", (env) =>
  end(env, job("organize-apply", "done", doneResult()), { before: { ...others, org: orgAnalysis("a1") }, full: true }));
add("organize-apply done without folders, S.org loaded: cfg is S.org.config", (env) =>
  end(env, job("organize-apply", "done", { result: { analysis: "a1" }, stats: { moved: 1 } }), { before: { org: orgAnalysis("a1") } }));
add("organize-apply done after a reload: S.org loaded, cfg from the loaded analysis", (env) =>
  end(env, job("organize-apply", "done", { result: { analysis: "a1" }, stats: { moved: 1 } }), { answers: { "/api/organize/analysis?id=a1": orgAnalysis("a1") } }));
add("organize-apply done after a reload, folders win over the loaded config", (env) =>
  end(env, job("organize-apply", "done", doneResult()), { answers: { "/api/organize/analysis?id=a1": orgAnalysis("a1") } }));
add("organize-apply done after a reload, get rejects: still done, S.org null, cfg from the result", (env) =>
  end(env, job("organize-apply", "done", doneResult()), { answers: { "/api/organize/analysis?id=a1": "reject" }, full: true }));
add("organize-apply done after a reload, get rejects, no folders: done without cfg", (env) =>
  end(env, job("organize-apply", "done", { result: { analysis: "a1" }, stats: { moved: 2 } }), { answers: { "/api/organize/analysis?id=a1": "reject" } }));
add("organize-apply done without result: no GET, done without cfg", (env) =>
  end(env, job("organize-apply", "done", { stats: { moved: 0 } })));
add("organize-apply done keeps the earlier notice and resets rem/chk", (env) =>
  end(env, job("organize-apply", "done", doneResult()), { before: { ...others, org: orgAnalysis("a1") } }));
add("organize-apply done, get pending: nothing drawn yet", (env) =>
  end(env, job("organize-apply", "done", doneResult()), { before: { view: "running", waitingFor: "job-organize-apply-done" } }));
add("organize-apply cancelled: done view stopped", (env) =>
  end(env, job("organize-apply", "cancelled", doneResult()), { before: { org: orgAnalysis("a1") }, full: true }));
add("organize-apply planChanged easy: S.org reloaded although set, open kept, no details", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a2", planChanged: true } }), { before: { ...others, org: orgAnalysis("a1") }, answers: { "/api/organize/analysis?id=a2": orgAnalysis("a2") } }));
add("organize-apply planChanged advanced: details from the job", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a2", planChanged: true } }), { mode: "advanced", before: { org: orgAnalysis("a1"), open: ["dups"] }, answers: { "/api/organize/analysis?id=a2": orgAnalysis("a2") } }));
add("organize-apply planChanged with folders and moves: planChanged wins", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a2", planChanged: true, ...folders }, stats: { moved: 2 } }), { answers: { "/api/organize/analysis?id=a2": orgAnalysis("a2") } }));
add("organize-apply planChanged, get rejects: setup with the error, S.org kept", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a2", planChanged: true } }), { before: { org: orgAnalysis("a1") }, answers: { "/api/organize/analysis?id=a2": "reject" } }));
add("tidy-apply planChanged easy: tidy texts, no details", (env) =>
  end(env, job("tidy-apply", "failed", { result: { analysis: "t2", planChanged: true } }), { before: { org: tidyAnalysis("t1"), open: ["text"] }, answers: { "/api/organize/analysis?id=t2": tidyAnalysis("t2") } }));
add("tidy-apply planChanged advanced: tidy texts with details", (env) =>
  end(env, job("tidy-apply", "failed", { result: { analysis: "t2", planChanged: true } }), { mode: "advanced", answers: { "/api/organize/analysis?id=t2": tidyAnalysis("t2") } }));
add("tidy-apply done: tidy done page", (env) =>
  end(env, job("tidy-apply", "done", { result: { analysis: "t1", tidy: true, ...folders, steps: [{ step: "songs", stats: { moved: 2 } }] }, stats: { moved: 2 } }), { before: { org: tidyAnalysis("t1") }, full: true }));
add("tidy-apply failed with folders and moves: the tidy branch wins over moved", (env) =>
  end(env, job("tidy-apply", "failed", { result: { analysis: "t1", tidy: true, ...folders, steps: [] }, stats: { moved: 4 } }), { before: { org: tidyAnalysis("t1") }, full: true }));
add("tidy-apply failed with folders, no moves: done, not everything could be done", (env) =>
  end(env, job("tidy-apply", "failed", { result: { analysis: "t1", tidy: true, ...folders, steps: [] }, stats: { moved: 0 } }), { before: { org: tidyAnalysis("t1") } }));
add("tidy-apply failed without folders, S.org null: loads the analysis, review", (env) =>
  end(env, job("tidy-apply", "failed", { result: { analysis: "t1" }, stats: { moved: 4 } }), { answers: { "/api/organize/analysis?id=t1": tidyAnalysis("t1") } }));
add("organize-apply failed with folders, no moves: not the done view", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a1", ...folders }, stats: { moved: 0 } }), { before: { org: orgAnalysis("a1") } }));
add("organize-apply failed with moves and folders: done with undo, moving did not finish", (env) =>
  end(env, job("organize-apply", "failed", doneResult()), { before: { ...others, org: orgAnalysis("a1") }, full: true }));
add("organize-apply failed with moves, no folders: review", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a1" }, stats: { moved: 2 } }), { before: { org: orgAnalysis("a1") } }));
add("organize-apply failed without moves, S.org set: review, no GET", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a1" } }), { before: { ...others, org: orgAnalysis("a1") } }));
add("organize-apply failed after a reload: analysis loaded, review", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a1" } }), { answers: { "/api/organize/analysis?id=a1": orgAnalysis("a1") } }));
add("organize-apply failed after a reload, get rejects: setup with the GET error", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: "a1" } }), { before: { notice: { kind: "", title: "earlier" } }, answers: { "/api/organize/analysis?id=a1": "reject" } }));
add("organize-apply failed after a reload without analysis: setup", (env) =>
  end(env, job("organize-apply", "failed", {}), { before: others }));
add("organize-apply with an unexpected state: the failed branches", (env) =>
  end(env, job("organize-apply", "odd", { result: { analysis: "a1" } }), { before: { org: orgAnalysis("a1") } }));

// ---------------------------------------------------------------- overview / export

const overviewResult = { library: LIB, outDir: OUT, songs: 2, bytes: 2048, withVideo: 1, withImage: 2, noAudio: 0, noSong: 0, unreadable: 0, buckets: [{ name: "A", songs: 2, bytes: 2048 }], setAside: { invalid: 1 } };
const exportResult = { id: "e 1", library: LIB, outDir: OUT, songs: 5, file: `${OUT}/song_list.csv` };

add("overview done: result stored, overview view, notice kept", (env) =>
  end(env, job("overview", "done", { result: overviewResult }), { before: others, full: true }));
add("overview cancelled: setup, Stopped.", (env) =>
  end(env, job("overview", "cancelled"), { before: { overview: overviewResult } }));
add("overview failed: setup, counting did not finish", (env) =>
  end(env, job("overview", "failed")));
add("export done: result stored, exported view", (env) =>
  end(env, job("export", "done", { result: exportResult }), { before: others, full: true }));
add("export cancelled: setup, Stopped.", (env) =>
  end(env, job("export", "cancelled"), { before: { exported: exportResult } }));
add("export failed: setup, the export did not finish", (env) =>
  end(env, job("export", "failed")));

// ---------------------------------------------------------------- originals-delete / setaside-restore

const originals = { config: { library: "/orig/lib", outDir: "/orig/out" }, dir: "/orig/out/originals", files: 3, bytes: 100, largest: [] };
const aside = { config: { library: "/aside/lib", outDir: "/aside/out" }, items: [] };

add("originals-delete done: cfg from S.originals, notice kept", (env) =>
  end(env, job("originals-delete", "done", { stats: { removed: 3 } }), { before: { originals, notice: { kind: "", title: "earlier" } }, full: true }));
add("originals-delete done with result folders: they win", (env) =>
  end(env, job("originals-delete", "done", { result: folders, stats: { removed: 3 } }), { before: { originals } }));
add("originals-delete done without S.originals: no cfg", (env) =>
  end(env, job("originals-delete", "done", { stats: { removed: 1 } })));
add("originals-delete cancelled: stopped, no new notice", (env) =>
  end(env, job("originals-delete", "cancelled", { stats: { removed: 1 } }), { before: { originals, notice: { kind: "", title: "earlier" } } }));
add("originals-delete failed: not everything could be deleted", (env) =>
  end(env, job("originals-delete", "failed", { stats: { removed: 1, failed: 2 } }), { before: { originals } }));
add("setaside-restore done: cfg from S.aside", (env) =>
  end(env, job("setaside-restore", "done", { stats: { moved: 2 } }), { before: { aside, ...others }, full: true }));
add("setaside-restore done with result folders: they win", (env) =>
  end(env, job("setaside-restore", "done", { result: folders, stats: { moved: 2 } }), { before: { aside } }));
add("setaside-restore cancelled: stopped, notice kept", (env) =>
  end(env, job("setaside-restore", "cancelled", { stats: { moved: 1 } }), { before: { aside, notice: { kind: "", title: "earlier" } } }));
add("setaside-restore failed: not everything could be put back", (env) =>
  end(env, job("setaside-restore", "failed", { stats: { moved: 1, failed: 1 } })));

// ---------------------------------------------------------------- check-analyse / check-apply

add("check-analyse done: loads S.chk, resets org/rem/open, keeps rmKeep and notice", (env) =>
  end(env, job("check-analyse", "done", { result: { analysis: "c1" } }), { before: { ...others, org: orgAnalysis("a1") }, answers: { "/api/check/analysis?id=c1": chkAnalysis("c1") }, full: true }));
add("check-analyse done: get rejects -> setup, org and rem stay", (env) =>
  end(env, job("check-analyse", "done", { result: { analysis: "c1" } }), { before: { ...others, org: orgAnalysis("a1") }, answers: { "/api/check/analysis?id=c1": "reject" } }));
add("check-analyse done: get pending -> org and rem still there", (env) =>
  end(env, job("check-analyse", "done", { result: { analysis: "c1" } }), { before: { ...others, org: orgAnalysis("a1"), view: "running" } }));
add("check-analyse cancelled: setup, Stopped., others kept", (env) =>
  end(env, job("check-analyse", "cancelled"), { before: { ...others, org: orgAnalysis("a1") } }));
add("check-analyse failed: setup, the check did not finish", (env) =>
  end(env, job("check-analyse", "failed"), { before: { org: orgAnalysis("a1") } }));
add("check-apply done: cfg from S.chk", (env) =>
  end(env, job("check-apply", "done", { stats: { moved: 1 } }), { before: { chk: chkAnalysis("c1"), notice: { kind: "", title: "earlier" } }, full: true }));
add("check-apply done with folders", (env) =>
  end(env, job("check-apply", "done", { result: folders, stats: { moved: 1 } }), { before: { chk: chkAnalysis("c1") } }));
add("check-apply cancelled: done stopped, notice kept", (env) =>
  end(env, job("check-apply", "cancelled", { stats: { moved: 0 } }), { before: { chk: chkAnalysis("c1"), notice: { kind: "", title: "earlier" } } }));
add("check-apply failed with moves and folders: done, not everything could be done", (env) =>
  end(env, job("check-apply", "failed", { result: folders, stats: { moved: 2 } }), { before: { chk: chkAnalysis("c1") }, full: true }));
add("check-apply failed with planChanged and moves: no planChanged branch", (env) =>
  end(env, job("check-apply", "failed", { result: { ...folders, planChanged: true, analysis: "c2" }, stats: { moved: 2 } }), { before: { chk: chkAnalysis("c1") } }));
add("check-apply failed with planChanged, no moves: review, no GET", (env) =>
  end(env, job("check-apply", "failed", { result: { planChanged: true, analysis: "c2" } }), { before: { chk: chkAnalysis("c1") } }));
add("check-apply failed with moves, no folders, S.chk: review", (env) =>
  end(env, job("check-apply", "failed", { stats: { moved: 2 } }), { before: { chk: chkAnalysis("c1") } }));
add("check-apply failed without S.chk: setup", (env) =>
  end(env, job("check-apply", "failed", { result: folders }), { before: { ...others, chk: null } }));

// ---------------------------------------------------------------- remove-analyse / remove-apply

add("remove-analyse done: loads S.rem, resets org/chk/open/rmKeep, keeps notice", (env) =>
  end(env, job("remove-analyse", "done", { result: { analysis: "r1" } }), { before: { ...others, org: orgAnalysis("a1") }, answers: { "/api/remove/analysis?id=r1": remAnalysis("r1") }, full: true }));
add("remove-analyse done: get rejects -> setup, org/chk/open/rmKeep stay", (env) =>
  end(env, job("remove-analyse", "done", { result: { analysis: "r1" } }), { before: { ...others, org: orgAnalysis("a1") }, answers: { "/api/remove/analysis?id=r1": "reject" } }));
add("remove-analyse cancelled: Stopped. Nothing was changed.", (env) =>
  end(env, job("remove-analyse", "cancelled"), { before: others }));
add("remove-analyse failed: the check did not finish", (env) =>
  end(env, job("remove-analyse", "failed")));
add("remove-apply done: cfg from S.rem", (env) =>
  end(env, job("remove-apply", "done", { stats: { moved: 1 } }), { before: { rem: remAnalysis("r1"), rmKeep: ["k"] }, full: true }));
add("remove-apply done with folders", (env) =>
  end(env, job("remove-apply", "done", { result: folders, stats: { moved: 1 } }), { before: { rem: remAnalysis("r1") } }));
add("remove-apply cancelled: stopped", (env) =>
  end(env, job("remove-apply", "cancelled", { stats: { moved: 1 } }), { before: { rem: remAnalysis("r1") } }));
add("remove-apply planChanged easy with S.chk left over: chk not reset, check review drawn", (env) =>
  end(env, job("remove-apply", "failed", { result: { analysis: "r2", planChanged: true } }), { before: { ...others, rem: remAnalysis("r1") }, answers: { "/api/remove/analysis?id=r2": remAnalysis("r2") } }));
add("remove-apply planChanged easy: S.rem reloaded, rmKeep reset, open kept, no details", (env) =>
  end(env, job("remove-apply", "failed", { result: { analysis: "r2", planChanged: true } }), { before: { rem: remAnalysis("r1"), rmKeep: ["k"], open: ["o"], notice: { kind: "", title: "earlier" } }, answers: { "/api/remove/analysis?id=r2": remAnalysis("r2") }, full: true }));
add("remove-apply planChanged advanced: details", (env) =>
  end(env, job("remove-apply", "failed", { result: { analysis: "r2", planChanged: true } }), { mode: "advanced", before: { rem: remAnalysis("r1"), rmKeep: ["k"], open: ["o"] }, answers: { "/api/remove/analysis?id=r2": remAnalysis("r2") } }));
add("remove-apply planChanged, get rejects: setup, rmKeep kept", (env) =>
  end(env, job("remove-apply", "failed", { result: { analysis: "r2", planChanged: true } }), { before: { rem: remAnalysis("r1"), rmKeep: ["k"] }, answers: { "/api/remove/analysis?id=r2": "reject" } }));
add("remove-apply failed with moves and folders: done, removing did not finish", (env) =>
  end(env, job("remove-apply", "failed", { result: folders, stats: { moved: 2 } }), { before: { rem: remAnalysis("r1") }, full: true }));
add("remove-apply failed with moves, no folders: review", (env) =>
  end(env, job("remove-apply", "failed", { stats: { moved: 2 } }), { before: { rem: remAnalysis("r1") } }));
add("remove-apply failed without S.rem: setup", (env) =>
  end(env, job("remove-apply", "failed", { result: { analysis: "r1" } }), { before: { org: orgAnalysis("a1") } }));

// ---------------------------------------------------------------- organize-undo

const undoBefore = { done: { kind: "organize", job: { id: "old" }, stopped: false, cfg: OLD }, org: orgAnalysis("a1"), originals, aside };
add("organize-undo done without folders: cfg null, never an earlier config", (env) =>
  end(env, job("organize-undo", "done", { stats: { moved: 2 } }), { before: undoBefore, full: true }));
add("organize-undo done with folders", (env) =>
  end(env, job("organize-undo", "done", { result: folders, stats: { moved: 2 } }), { before: undoBefore }));
add("organize-undo cancelled: no new notice", (env) =>
  end(env, job("organize-undo", "cancelled", { result: folders, stats: { moved: 1 } }), { before: { ...undoBefore, notice: { kind: "", title: "earlier" } }, full: true }));
add("organize-undo failed: undo did not finish", (env) =>
  end(env, job("organize-undo", "failed", { result: folders, stats: { moved: 1, failed: 1 } }), { full: true }));
add("tidy-undo: mapped to organize-undo", (env) =>
  end(env, job("tidy-undo", "done", { stats: { moved: 0 } })));

// ---------------------------------------------------------------- encoded analysis ids, unexpected states, folders without a library

const ENC = "a b/&?";
const encQ = "a%20b%2F%26%3F";
add("check-analyse done: analysis id is URL-encoded", (env) =>
  end(env, job("check-analyse", "done", { result: { analysis: ENC } }), { answers: { [`/api/check/analysis?id=${encQ}`]: chkAnalysis("c1") } }));
add("remove-analyse done: analysis id is URL-encoded", (env) =>
  end(env, job("remove-analyse", "done", { result: { analysis: ENC } }), { answers: { [`/api/remove/analysis?id=${encQ}`]: remAnalysis("r1") } }));
add("remove-apply planChanged: analysis id is URL-encoded", (env) =>
  end(env, job("remove-apply", "failed", { result: { analysis: ENC, planChanged: true } }), { before: { rem: remAnalysis("r1") }, answers: { [`/api/remove/analysis?id=${encQ}`]: remAnalysis("r2") } }));
add("organize-apply done after a reload: analysis id is URL-encoded", (env) =>
  end(env, job("organize-apply", "done", { result: { analysis: ENC }, stats: { moved: 1 } }), { answers: { [`/api/organize/analysis?id=${encQ}`]: orgAnalysis("a1") } }));
add("organize-apply planChanged: analysis id is URL-encoded", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: ENC, planChanged: true } }), { before: { org: orgAnalysis("a1") }, answers: { [`/api/organize/analysis?id=${encQ}`]: orgAnalysis("a2") } }));
add("organize-apply failed after a reload: analysis id is URL-encoded", (env) =>
  end(env, job("organize-apply", "failed", { result: { analysis: ENC } }), { answers: { [`/api/organize/analysis?id=${encQ}`]: orgAnalysis("a1") } }));
add("remove-apply with an unexpected state: the failed chain, not done", (env) =>
  end(env, job("remove-apply", "odd", { stats: { moved: 0 } }), { before: { rem: remAnalysis("r1") } }));
add("check-apply with an unexpected state, no moves: review, the check did not finish", (env) =>
  end(env, job("check-apply", "odd", { stats: { moved: 0 } }), { before: { chk: chkAnalysis("c1") } }));
add("check-apply with an unexpected state, moves and folders: done, earlier notice kept", (env) =>
  end(env, job("check-apply", "odd", { result: folders, stats: { moved: 2 } }), { before: { chk: chkAnalysis("c1"), notice: { kind: "", title: "earlier" } } }));
add("originals-delete with an unexpected state: earlier notice kept, no failed notice", (env) =>
  end(env, job("originals-delete", "odd", { stats: { removed: 1 } }), { before: { originals, notice: { kind: "", title: "earlier" } } }));
add("setaside-restore with an unexpected state: earlier notice kept, no failed notice", (env) =>
  end(env, job("setaside-restore", "odd", { stats: { moved: 1 } }), { before: { aside, notice: { kind: "", title: "earlier" } } }));
add("organize-undo with an unexpected state: earlier notice kept, no failed notice", (env) =>
  end(env, job("organize-undo", "odd", { result: folders, stats: { moved: 1 } }), { before: { ...undoBefore, notice: { kind: "", title: "earlier" } } }));
add("organize-apply done with an outDir but no library: cfg is S.org.config", (env) =>
  end(env, job("organize-apply", "done", { result: { analysis: "a1", outDir: OUT }, stats: { moved: 1 } }), { before: { org: orgAnalysis("a1") } }));
add("originals-delete done with an outDir but no library: cfg from S.originals", (env) =>
  end(env, job("originals-delete", "done", { result: { outDir: OUT }, stats: { removed: 1 } }), { before: { originals } }));

// ---------------------------------------------------------------- unknown kinds, no kind

for (const kind of ["something-else", "constructor", "toString", "__proto__", "hasOwnProperty", "tidy-constructor", "valueOf", ""]) {
  add(`kind ${JSON.stringify(kind)}: setup, nothing else changes`, (env) =>
    end(env, job(kind, "done", { result: { analysis: "a1", ...folders } }), { before: { ...others, view: "running" } }));
}
add("job without kind: setup with the TypeError message, scrolled", (env) =>
  end(env, { id: "nokind", state: "done", result: { analysis: "a1" } }, { before: { ...others, view: "running", waitingFor: "nokind" } }));
add("job with kind null: setup with the TypeError message", (env) =>
  end(env, { id: "nullkind", kind: null, state: "failed" }));
add("tidy-analyse with an unknown tidy kind 'tidy-x': setup", (env) =>
  end(env, job("tidy-x", "failed")));

// ---------------------------------------------------------------- the guard

add("a job handled before: nothing happens", (env) =>
  end(env, job("overview", "done", { result: overviewResult }), { before: { handled: ["job-overview-done"], view: "running", waitingFor: "other" } }));
add("the same job twice while its GET is pending: handled once", async (env) => {
  let resolve;
  const deferred = () => new Promise((r) => { resolve = r; });
  const j = job("organize-analyse", "done", { result: { analysis: "a1" } });
  return end(env, j, {
    before: { view: "running", waitingFor: j.id },
    answers: { "/api/organize/analysis?id=a1": deferred },
    again: async (env) => {
      const pending = { state: env.state(["view", "waitingFor", "handled"]), requests: env.requests.filter((r) => r.url.includes("analysis")).length };
      env.run("globalThis.__second = false; onJobEnd(__j).then(() => { globalThis.__second = true; })");
      await env.flush();
      pending.secondSettled = env.run("__second");
      pending.requestsAfterSecond = env.requests.filter((r) => r.url.includes("analysis")).length;
      env.ctx.__pending = pending;
      resolve(env.reply(200, orgAnalysis("a1")));
      await env.flush();
    },
  }).then((out) => ({ ...out, whilePending: env.ctx.__pending }));
});
add("the same job twice in one tick (a replayed event): one GET, one scroll", async (env) => {
  const j = job("organize-analyse", "done", { result: { analysis: "a1" } });
  return end(env, j, {
    before: { view: "running", waitingFor: j.id },
    again: async (env) => {
      env.ctx.__j2 = job("overview", "done", { result: overviewResult });
      env.run("onJobEnd(__j2); onJobEnd(__j2)");
      await env.flush();
    },
  });
});
add("the same job twice synchronously while a GET is needed: one GET", async (env) => {
  env.ctx.__j = job("check-analyse", "done", { result: { analysis: "c1" } });
  env.answers["/api/check/analysis?id=c1"] = () => env.reply(200, chkAnalysis("c1"));
  const base = env.requests.length;
  env.run("onJobEnd(__j); onJobEnd(__j)");
  await env.flush();
  return { state: env.state(KEYS), requests: env.requests.slice(base).map((r) => r.url), scrolls: env.scrolls, page: page(env) };
});
add("two different jobs: both handled, the later one draws", async (env) => {
  const j1 = job("overview", "done", { result: overviewResult });
  return end(env, j1, {
    again: async (env) => {
      env.ctx.__j = job("export", "failed");
      env.run("onJobEnd(__j)");
      await env.flush();
    },
  });
});

module.exports = cases;
