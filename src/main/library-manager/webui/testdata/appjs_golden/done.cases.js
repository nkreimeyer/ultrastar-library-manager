// Golden cases of the done page: viewDone and doneTitle/movedText. Each case puts a done
// object (as the S.done setters in onJobEnd build them) into S, renders the page and
// snapshots it, or clicks one of its buttons and records what that did.
"use strict";

const CFG = { library: "/music/lib", outDir: "/music/organize-output" };
const OTHER_CFG = { library: "/other/lib", outDir: "/other/out" };
const NOTICE = { kind: "ok", title: "Something to know", text: "A notice text.", details: ["/a/b", "/c/d"] };

// show renders the done page for d and returns the page's snapshot.
function show(env, d, { mode = "easy", notice = null, aside = null, sys = null } = {}) {
  env.set("S.mode", mode);
  env.set("S.notice", notice);
  env.set("S.aside", aside);
  if (sys) env.set("S.sys", sys);
  env.set("S.done", d);
  env.set("S.view", "done");
  env.run("render()");
  return env.snap(env.app().firstChild);
}

// page returns the snapshot plus the button labels and the plain text, so the goldens read
// easily.
function page(env, d, opts) {
  const s = show(env, d, opts);
  return { buttons: env.buttons(env.app()), text: env.app().textContent, snap: s };
}

// sent lists the requests after the start-up /api/system call.
const sent = (env) => env.requests.filter((r) => r.url !== "/api/system");

const job = (state, stats, result) => ({ id: "j1", state, stats, ...(result ? { result } : {}) });
const org = (state, stats, extra = {}) => ({ kind: "organize", job: job(state, stats), stopped: state === "cancelled", cfg: CFG, ...extra });
const tidy = (state, steps, stats = { moved: 0, failed: 0 }, extra = {}) => ({
  kind: "organize", job: job(state, stats, { tidy: true, library: CFG.library, outDir: CFG.outDir, steps }),
  stopped: state === "cancelled", cfg: CFG, ...extra,
});
const undoDone = (state, stats, extra = {}) => ({ kind: "undo", job: job(state, stats), cfg: CFG, ...extra });

const ALL_STEPS = [
  { step: "text", stats: { written: 3, moved: 1 } },
  { step: "cleanup", stats: { moved: 2 } },
  { step: "songs", stats: { moved: 4 } },
  { step: "video", stats: { replaced: 1, moved: 1 } },
  { step: "audio", stats: { replaced: 1 } },
];
const ONE_EACH = [
  { step: "text", stats: { written: 1, moved: 1 } },
  { step: "cleanup", stats: { moved: 1 } },
  { step: "songs", stats: { moved: 1 } },
  { step: "video", stats: { replaced: 1, moved: 0 } },
  { step: "audio", stats: { replaced: 1 } },
];
const ZERO_STEPS = [
  { step: "text", stats: { written: 0, moved: 0 } },
  { step: "cleanup", stats: { moved: 0 } },
  { step: "songs", stats: { moved: 0 } },
  { step: "video", stats: { replaced: 0, moved: 0 } },
  { step: "audio", stats: { replaced: 0 } },
];

// clickCase renders d, clicks the button labelled label and returns what happened.
async function clickCase(env, d, label, { opts = {}, before, keys = ["view", "notice", "reportsFrom", "asideFrom"] } = {}) {
  env.set("S.f.library", "/music/lib");
  if (before) before(env);
  show(env, d, opts);
  const btn = env.button(env.app(), label);
  if (!btn) throw new Error(`no button ${label}`);
  await env.click(btn);
  const h1 = env.find(env.app(), (e) => e.tagName === "h1");
  return { state: env.state(keys), requests: sent(env), h1: h1 ? h1.textContent : null };
}

module.exports = [
  // ------------------------------------------------------------ doneTitle / movedText
  { name: "doneTitle variants", run: (env) => {
    env.set("__ds", [
      { stopped: true, job: { state: "done" } },
      { stopped: true, job: { state: "failed" } },
      { stopped: false, job: { state: "done" } },
      { stopped: false, job: { state: "failed" } },
      { stopped: false, job: { state: "cancelled" } },
    ]);
    return env.run("__ds.map(doneTitle)");
  } },
  { name: "movedText variants", run: (env) => {
    env.set("__ds", [
      { stopped: true, job: { state: "cancelled", stats: { moved: 1 } } },
      { stopped: true, remove: true, job: { state: "cancelled", stats: { moved: 3 } } },
      { stopped: false, job: { state: "failed", stats: { moved: 1 } } },
      { stopped: false, job: { state: "failed", stats: { moved: 2 } } },
      { stopped: false, remove: true, job: { state: "failed", stats: { moved: 1 } } },
      { stopped: false, job: { state: "done", stats: { moved: 0 } } },
      { stopped: false, job: { state: "done", stats: { moved: 1 } } },
      { stopped: false, job: { state: "done", stats: { moved: 1234 } } },
      { stopped: false, remove: true, job: { state: "done", stats: { moved: 1 } } },
      { stopped: false, remove: true, job: { state: "done", stats: { moved: 7 } } },
      { stopped: false, job: { state: "done" } },
    ]);
    return env.run("__ds.map(movedText)");
  } },

  // ------------------------------------------------------------ tidy
  { name: "tidy done all steps easy", run: (env) => page(env, tidy("done", ALL_STEPS, { moved: 8, failed: 0 })) },
  { name: "tidy done one of each (singular)", run: (env) => page(env, tidy("done", ONE_EACH, { moved: 3, failed: 0 })) },
  { name: "tidy done songs 0 but other moves: no undo, no stray 0", run: (env) => page(env, tidy("done", [
    { step: "text", stats: { written: 2, moved: 5 } }, { step: "cleanup", stats: { moved: 1 } }, { step: "songs", stats: { moved: 0 } },
  ], { moved: 6, failed: 0 })) },
  { name: "tidy done nothing changed", run: (env) => page(env, tidy("done", ZERO_STEPS)) },
  { name: "tidy failed nothing changed shows no paragraph", run: (env) => page(env, tidy("failed", ZERO_STEPS)) },
  { name: "tidy done no steps field", run: (env) => page(env, tidy("done", undefined)) },
  { name: "tidy done no job stats", run: (env) => {
    const d = tidy("done", ALL_STEPS);
    delete d.job.stats;
    return page(env, d);
  } },
  { name: "tidy songs step with empty stats", run: (env) => page(env, tidy("done", [{ step: "songs", stats: {} }, { step: "video", stats: {} }])) },
  { name: "tidy songs step without stats throws", throws: true, run: (env) => page(env, tidy("done", [{ step: "songs" }])) },
  { name: "tidy without songs step", run: (env) => page(env, tidy("done", [{ step: "text", stats: { written: 1 } }])) },
  { name: "tidy unknown step name", run: (env) => page(env, tidy("done", [{ step: "mystery", stats: { moved: 9 } }])) },
  { name: "tidy step errors easy no notice", run: (env) => page(env, tidy("failed", [
    { step: "text", stats: { written: 1 } }, { step: "video", stats: {}, error: "ffmpeg missing" }, { step: "mystery", stats: {}, error: "boom" },
  ])) },
  { name: "tidy one step error", run: (env) => page(env, tidy("failed", [{ step: "cleanup", stats: {}, error: "cannot read" }])) },
  { name: "tidy step errors hidden when S.notice set", run: (env) => page(env, tidy("failed", [
    { step: "video", stats: {}, error: "ffmpeg missing" },
  ]), { notice: NOTICE }) },
  { name: "tidy failed files easy", run: (env) => page(env, tidy("failed", ALL_STEPS, { moved: 8, failed: 2 })) },
  { name: "tidy failed one file advanced", run: (env) => page(env, tidy("done", ALL_STEPS, { moved: 8, failed: 1 }), { mode: "advanced" }) },
  { name: "tidy stopped", run: (env) => page(env, tidy("cancelled", ALL_STEPS, { moved: 8, failed: 0 })) },
  { name: "tidy cancelled state but not stopped", run: (env) => page(env, tidy("cancelled", ALL_STEPS, { moved: 1 }, { stopped: false })) },
  { name: "tidy without cfg", run: (env) => page(env, tidy("done", ALL_STEPS, { moved: 8 }, { cfg: null })) },
  { name: "tidy with notice and errors and failures advanced", run: (env) => page(env, tidy("failed", [
    { step: "songs", stats: { moved: 2 } }, { step: "audio", stats: {}, error: "bad" },
  ], { moved: 2, failed: 3 }), { mode: "advanced", notice: { kind: "bad", title: "Oops" } }) },
  { name: "tidy wins over originals restore check flags", run: (env) => page(env, tidy("done", ALL_STEPS, { moved: 8, removed: 3 }, { originals: true, restore: true, check: true, remove: true })) },
  { name: "tidy result on undo kind renders undo page", run: (env) => {
    const d = tidy("done", ALL_STEPS, { moved: 2 });
    d.kind = "undo";
    return page(env, d);
  } },

  // ------------------------------------------------------------ originals
  { name: "originals done with cfg has only main menu", run: (env) => page(env, org("done", { removed: 3, failed: 0, moved: 2 }, { originals: true })) },
  { name: "originals one removed", run: (env) => page(env, org("done", { removed: 1 }, { originals: true })) },
  { name: "originals zero removed", run: (env) => page(env, org("done", { removed: 0, failed: 0 }, { originals: true })) },
  { name: "originals failed state but failed 0 is Done", run: (env) => page(env, org("failed", { removed: 2, failed: 0 }, { originals: true })) },
  { name: "originals failed one easy", run: (env) => page(env, org("done", { removed: 2, failed: 1 }, { originals: true })) },
  { name: "originals failed two advanced", run: (env) => page(env, org("failed", { removed: 0, failed: 2 }, { originals: true }), { mode: "advanced" }) },
  { name: "originals stopped", run: (env) => page(env, org("cancelled", { removed: 4, failed: 1 }, { originals: true })) },
  { name: "originals no stats no cfg with notice", run: (env) => {
    const d = org("done", undefined, { originals: true, cfg: null });
    return page(env, d, { notice: NOTICE });
  } },
  { name: "originals wins over restore and check", run: (env) => page(env, org("done", { removed: 1, moved: 1 }, { originals: true, restore: true, check: true })) },
  { name: "originals flag on undo kind", run: (env) => page(env, { kind: "undo", originals: true, job: job("done", { removed: 1, moved: 1 }), cfg: CFG }) },

  // ------------------------------------------------------------ restore
  { name: "restore done moved 2 with cfg", run: (env) => page(env, org("done", { moved: 2, failed: 0 }, { restore: true })) },
  { name: "restore done moved 1", run: (env) => page(env, org("done", { moved: 1 }, { restore: true })) },
  { name: "restore moved 0 with cfg no undo", run: (env) => page(env, org("done", { moved: 0, failed: 0 }, { restore: true })) },
  { name: "restore moved 3 without cfg", run: (env) => page(env, org("done", { moved: 3 }, { restore: true, cfg: null })) },
  { name: "restore done failed 1 easy", run: (env) => page(env, org("done", { moved: 1, failed: 1 }, { restore: true })) },
  { name: "restore failed 2 advanced", run: (env) => page(env, org("failed", { moved: 0, failed: 2 }, { restore: true }), { mode: "advanced" }) },
  { name: "restore failed 1 advanced", run: (env) => page(env, org("done", { moved: 0, failed: 1 }, { restore: true }), { mode: "advanced" }) },
  { name: "restore failed state failed 0", run: (env) => page(env, org("failed", { moved: 2, failed: 0 }, { restore: true })) },
  { name: "restore stopped", run: (env) => page(env, org("cancelled", { moved: 2, failed: 0 }, { restore: true })) },
  { name: "restore with notice and aside", run: (env) => page(env, org("done", { moved: 1 }, { restore: true }), { notice: NOTICE, aside: { config: CFG, items: [] } }) },
  { name: "restore wins over check and remove", run: (env) => page(env, org("done", { moved: 2 }, { restore: true, check: true, remove: true })) },

  // ------------------------------------------------------------ check
  { name: "check done moved 2 with cfg", run: (env) => page(env, org("done", { moved: 2, failed: 0 }, { check: true })) },
  { name: "check done moved 1", run: (env) => page(env, org("done", { moved: 1 }, { check: true })) },
  { name: "check moved 0 no cfg", run: (env) => page(env, org("done", { moved: 0, failed: 0 }, { check: true, cfg: null })) },
  { name: "check windows path", run: (env) => page(env, org("done", { moved: 2 }, { check: true, cfg: { library: "C:\\lib", outDir: "C:\\out" } }), { sys: { os: "windows", tools: [] } }) },
  { name: "check out dir with trailing separator", run: (env) => page(env, org("done", { moved: 2 }, { check: true, cfg: { library: "/lib", outDir: "/out/" } })) },
  { name: "check failed 1 easy", run: (env) => page(env, org("failed", { moved: 1, failed: 1 }, { check: true })) },
  { name: "check failed 3 advanced", run: (env) => page(env, org("done", { moved: 0, failed: 3 }, { check: true }), { mode: "advanced" }) },
  { name: "check stopped with notice", run: (env) => page(env, org("cancelled", { moved: 2 }, { check: true }), { notice: NOTICE }) },
  { name: "check wins over remove", run: (env) => page(env, org("done", { moved: 2 }, { check: true, remove: true })) },
  { name: "check flag on undo kind", run: (env) => page(env, { kind: "undo", check: true, job: job("done", { moved: 1 }), cfg: CFG }) },

  // ------------------------------------------------------------ organize
  { name: "organize done moved 5", run: (env) => page(env, org("done", { moved: 5, failed: 0 })) },
  { name: "organize done moved 1", run: (env) => page(env, org("done", { moved: 1 })) },
  { name: "organize done moved 0 with cfg no undo", run: (env) => page(env, org("done", { moved: 0, failed: 0 })) },
  { name: "organize moved 3 without cfg", run: (env) => page(env, org("done", { moved: 3 }, { cfg: null })) },
  { name: "organize stopped moved 2", run: (env) => page(env, org("cancelled", { moved: 2 })) },
  { name: "organize failed moved 1 failed 1 easy", run: (env) => page(env, org("failed", { moved: 1, failed: 1 })) },
  { name: "organize failed moves 2 advanced", run: (env) => page(env, org("failed", { moved: 0, failed: 2 }), { mode: "advanced" }) },
  { name: "organize no stats", run: (env) => page(env, org("done", undefined)) },
  { name: "organize with notice", run: (env) => page(env, org("done", { moved: 2 }), { notice: NOTICE }) },
  { name: "organize with bad notice no title", run: (env) => page(env, org("done", { moved: 2 }), { notice: { kind: "bad", text: "only text" } }) },
  { name: "remove done moved 2", run: (env) => page(env, org("done", { moved: 2, failed: 0 }, { remove: true })) },
  { name: "remove done moved 1 windows", run: (env) => page(env, org("done", { moved: 1 }, { remove: true, cfg: { library: "D:\\l", outDir: "D:\\o" } }), { sys: { os: "windows", tools: [] } }) },
  { name: "remove done moved 0", run: (env) => page(env, org("done", { moved: 0 }, { remove: true })) },
  { name: "remove failed state", run: (env) => page(env, org("failed", { moved: 2, failed: 1 }, { remove: true })) },
  { name: "remove stopped", run: (env) => page(env, org("cancelled", { moved: 1 }, { remove: true })) },
  { name: "remove without cfg", run: (env) => page(env, org("done", { moved: 2 }, { remove: true, cfg: null })) },

  // ------------------------------------------------------------ undo
  { name: "undo done moved 3", run: (env) => page(env, undoDone("done", { moved: 3, failed: 0 })) },
  { name: "undo done moved 1", run: (env) => page(env, undoDone("done", { moved: 1 })) },
  { name: "undo done moved 0", run: (env) => page(env, undoDone("done", { moved: 0, failed: 0 })) },
  { name: "undo cancelled moved 2", run: (env) => page(env, undoDone("cancelled", { moved: 2, failed: 0 })) },
  { name: "undo cancelled no cfg", run: (env) => page(env, undoDone("cancelled", { moved: 2 }, { cfg: null })) },
  { name: "undo failed with failures easy", run: (env) => page(env, undoDone("failed", { moved: 1, failed: 2 })) },
  { name: "undo failed one advanced", run: (env) => page(env, undoDone("failed", { moved: 0, failed: 1 }), { mode: "advanced" }) },
  { name: "undo failed state failed 0 no retry", run: (env) => page(env, undoDone("failed", { moved: 2, failed: 0 })) },
  { name: "undo done with failure shows retry", run: (env) => page(env, undoDone("done", { moved: 2, failed: 1 })) },
  { name: "undo cancelled with failures", run: (env) => page(env, undoDone("cancelled", { moved: 1, failed: 1 })) },
  { name: "undo ignores d.stopped", run: (env) => page(env, undoDone("done", { moved: 2 }, { stopped: true })) },
  { name: "undo no stats with notice", run: (env) => page(env, undoDone("done", undefined), { notice: NOTICE }) },

  // ------------------------------------------------------------ unknown
  { name: "unknown kind renders empty div", run: (env) => page(env, { kind: "x", job: { state: "done" } }) },
  { name: "unknown kind with cfg and stats", run: (env) => page(env, { kind: "x", job: job("failed", { moved: 2, failed: 1 }), cfg: CFG }, { notice: NOTICE }) },
  { name: "viewDone builds a div directly", run: (env) => {
    env.set("S.done", org("done", { moved: 1 }));
    return env.snap(env.run("viewDone()"));
  } },
  { name: "viewDone without S.done throws", throws: true, run: (env) => {
    env.set("S.done", null);
    return env.run("viewDone()");
  } },

  // ------------------------------------------------------------ clicks
  { name: "click main menu from organize clears notice and validates", run: (env) => clickCase(env, org("done", { moved: 2 }), "Back to the Main Menu", { opts: { notice: NOTICE } }) },
  { name: "click main menu from undo with restore task", run: (env) => clickCase(env, undoDone("done", { moved: 2 }), "Back to the Main Menu", {
    opts: { notice: NOTICE }, before: (e) => e.set("S.task", "restore"),
  }) },
  { name: "click main menu from originals", run: (env) => clickCase(env, org("done", { removed: 1 }, { originals: true }), "Back to the Main Menu") },
  { name: "click main menu from restore", run: (env) => clickCase(env, org("done", { moved: 1 }, { restore: true }), "Back to the Main Menu", { opts: { notice: NOTICE, aside: { config: CFG, items: [] } } }) },
  { name: "click main menu from tidy", run: (env) => clickCase(env, tidy("done", ALL_STEPS), "Back to the Main Menu", { opts: { notice: NOTICE } }) },
  { name: "click main menu from check", run: (env) => clickCase(env, org("done", { moved: 1 }, { check: true }), "Back to the Main Menu") },
  { name: "click undo this run organize", run: (env) => clickCase(env, org("done", { moved: 2 }), "Undo this run", { opts: { notice: NOTICE } }) },
  { name: "click undo this run remove", run: (env) => clickCase(env, org("done", { moved: 2 }, { remove: true }), "Undo this run") },
  { name: "click undo uses the rendered cfg not S.done at click time", run: async (env) => {
    env.set("S.f.library", "/music/lib");
    show(env, org("done", { moved: 2 }));
    env.set("S.done", { kind: "organize", job: job("done", { moved: 1 }), cfg: OTHER_CFG });
    await env.click(env.button(env.app(), "Undo this run"));
    return sent(env);
  } },
  { name: "click each undo button uses the rendered cfg", run: async (env) => {
    const out = {};
    for (const [label, d] of [
      ["Put back the set-aside songs", tidy("done", ALL_STEPS)],
      ["Undo this run", org("done", { moved: 1 }, { restore: true })],
      ["Undo this run", org("done", { moved: 1 }, { remove: true })],
      ["Undo again", undoDone("cancelled", { moved: 1 })],
      ["Try the undo again", undoDone("failed", { moved: 1, failed: 1 })],
    ]) {
      env.requests.length = 0;
      show(env, d);
      env.set("S.done", { kind: "undo", job: job("cancelled", { moved: 1, failed: 1 }), cfg: OTHER_CFG });
      await env.click(env.button(env.app(), label));
      out[`${label} (${d.kind}${d.restore ? " restore" : ""}${d.remove ? " remove" : ""}${d.job.result?.tidy ? " tidy" : ""})`] = sent(env);
    }
    return out;
  } },
  { name: "click read the reports uses the rendered cfg", run: async (env) => {
    show(env, org("done", { moved: 1 }));
    env.set("S.done", org("done", { moved: 1 }, { cfg: OTHER_CFG }));
    await env.click(env.button(env.app(), "Read the reports"));
    return sent(env);
  } },
  { name: "click undo this run answered goes to running", run: async (env) => {
    env.answers["/api/organize/undo"] = () => env.reply(202, { id: "u1", kind: "organize-undo", state: "running" });
    return clickCase(env, org("done", { moved: 2 }), "Undo this run", { opts: { notice: NOTICE }, keys: ["view", "notice", "waitingFor"] });
  } },
  { name: "click put back set-aside songs tidy", run: (env) => clickCase(env, tidy("done", ALL_STEPS), "Put back the set-aside songs") },
  { name: "click undo this run restore", run: (env) => clickCase(env, org("done", { moved: 1 }, { restore: true }), "Undo this run") },
  { name: "click undo again after a stopped undo", run: (env) => clickCase(env, undoDone("cancelled", { moved: 1 }), "Undo again") },
  { name: "click try the undo again", run: (env) => clickCase(env, undoDone("failed", { moved: 1, failed: 1 }), "Try the undo again") },
  { name: "click read the reports organize", run: (env) => clickCase(env, org("done", { moved: 1 }), "Read the reports", { opts: { notice: NOTICE } }) },
  { name: "click read the reports answered", run: async (env) => {
    env.answers["/api/reports/list"] = () => env.reply(200, { config: CFG, files: [{ name: "organize_plan.txt", size: 10 }] });
    return clickCase(env, undoDone("done", { moved: 1 }), "Read the reports", { opts: { notice: NOTICE } });
  } },
  { name: "click read the reports tidy", run: (env) => clickCase(env, tidy("done", ALL_STEPS), "Read the reports") },
  { name: "click read the reports check", run: (env) => clickCase(env, org("done", { moved: 1 }, { check: true }), "Read the reports") },
  { name: "click read the reports restore", run: (env) => clickCase(env, org("done", { moved: 1 }, { restore: true }), "Read the reports") },
  { name: "click back to the list with aside loads the list, keeps notice", run: (env) => clickCase(env, org("done", { moved: 1 }, { restore: true }), "Back to the list", {
    opts: { notice: NOTICE, aside: { config: CFG, items: [] } },
  }) },
  { name: "click back to the list with aside answered", run: (env) => {
    env.answers["/api/setaside/list"] = () => env.reply(200, { config: CFG, items: [] });
    return clickCase(env, org("done", { moved: 1 }, { restore: true }), "Back to the list", {
      opts: { notice: NOTICE, aside: { config: OTHER_CFG, items: [] } }, before: (e) => e.set("S.task", "restore"),
    });
  } },
  { name: "click back to the list without aside goes to setup, keeps notice", run: (env) => clickCase(env, org("done", { moved: 1 }, { restore: true }), "Back to the list", {
    opts: { notice: NOTICE }, before: (e) => e.set("S.task", "restore"),
  }) },

  // ------------------------------------------------------------ audit survivors
  // A stopped tidy-up with nothing done says nothing about "Nothing needed to be changed".
  { name: "tidy cancelled zero steps has no nothing-changed line", run: (env) => page(env, tidy("cancelled", ZERO_STEPS)) },
  { name: "undo cancelled moved 0", run: (env) => page(env, undoDone("cancelled", { moved: 0, failed: 0 })) },
  // d.stopped, not the job state, decides "before you stopped" and the stopped titles.
  { name: "restore cancelled but not stopped", run: (env) => page(env, org("cancelled", { moved: 2 }, { restore: true, stopped: false })) },
  { name: "restore failed but stopped", run: (env) => page(env, org("failed", { moved: 2, failed: 1 }, { restore: true, stopped: true })) },
  { name: "originals cancelled but not stopped", run: (env) => page(env, org("cancelled", { removed: 2 }, { originals: true, stopped: false })) },
  { name: "originals done but stopped", run: (env) => page(env, org("done", { removed: 2 }, { originals: true, stopped: true })) },
  { name: "check cancelled but not stopped", run: (env) => page(env, org("cancelled", { moved: 2 }, { check: true, stopped: false })) },
  { name: "organize cancelled but not stopped", run: (env) => page(env, org("cancelled", { moved: 2 }, { stopped: false })) },
  { name: "remove done but stopped", run: (env) => page(env, org("done", { moved: 2 }, { remove: true, stopped: true })) },
  { name: "tidy done but stopped", run: (env) => page(env, tidy("done", ALL_STEPS, { moved: 4, failed: 0 }, { stopped: true })) },
  // The undo buttons read the cfg of the drawn done object when clicked.
  ...[
    ["organize", () => org("done", { moved: 2 }), "Undo this run"],
    ["restore", () => org("done", { moved: 2 }, { restore: true }), "Undo this run"],
    ["tidy", () => tidy("done", ALL_STEPS, { moved: 4, failed: 0 }), "Put back the set-aside songs"],
    ["undo", () => undoDone("cancelled", { moved: 1, failed: 0 }), "Undo again"],
  ].map(([kind, make, label]) => ({ name: `click ${kind} undo reads d.cfg at click time`, async run(env) {
    const d = make();
    show(env, d);
    const btn = env.button(env.app(), label);
    env.run("S.done.cfg = { library: '/later/lib', outDir: '/later/out' }");
    await env.click(btn);
    return sent(env).filter((r) => r.url === "/api/organize/undo");
  } })),
];
