// Golden cases of the setup page: viewSetup, the task radios, organizeRequest, validate,
// updateResolved, the start buttons, startAnalysis and the startup task choice.
"use strict";

const SYS = { os: "linux", toolsOK: true, tools: [] };
const TASK_KEYS = ["dedupe", "add", "overview", "originals", "reports", "restore", "check", "remove", "export", "organize"];

// A form with every field set, so snapshots and requests show what each task reads.
const FULL = {
  library: " /lib ",
  out: " /out ",
  tiers: [[" /a ", ""], ["/b"], [""]],
  patterns: "  ABBA \n",
  priority: true,
  ignoreThe: true,
  deep: true,
};

// assign copies plain values into the page's S.f.
function assign(env, f) {
  env.ctx.__f = JSON.parse(JSON.stringify(f));
  env.run("Object.assign(S.f, __f)");
  delete env.ctx.__f;
}

// show draws the setup page for mode and task with the form f.
async function show(env, { mode = "easy", task = "dedupe", f = FULL, sys = SYS, view = "setup" } = {}) {
  env.set("S.mode", mode);
  env.set("S.task", task);
  env.set("S.sys", sys);
  env.set("S.view", view);
  assign(env, f);
  env.run("render()");
  await env.flush();
}

// since returns the requests made after the page loaded (the /api/system call is first).
const since = (env, mark) => env.requests.slice(mark);
const startBtn = (env) => env.byId("start");
const resolvedText = (env) => ({ text: env.byId("resolved").textContent, disabled: startBtn(env).disabled, html: env.snap(env.byId("resolved")) });

function input(env, el, value) {
  el.value = value;
  el.dispatch("input");
}
const libraryInput = (env) => env.find(env.app(), (e) => e.tagName === "input" && (e.attrs["aria-label"] === "Your library" || e.attrs["aria-label"] === "New library folder"));

const cases = [];

// ---------------------------------------------------------------- viewSetup snapshots

for (const mode of ["easy", "advanced"]) {
  for (const task of TASK_KEYS) {
    cases.push({
      name: `viewSetup ${task} ${mode}`,
      async run(env) {
        await show(env, { mode, task });
        return env.snap(env.app());
      },
    });
  }
}

cases.push({
  name: "viewSetup unknown task renders no fields and Check first",
  async run(env) {
    await show(env, { mode: "advanced", task: "bogus" });
    return env.snap(env.app());
  },
});

cases.push({
  name: "viewSetup constructor task easy",
  async run(env) {
    await show(env, { task: "constructor" });
    return env.snap(env.app());
  },
});

cases.push({
  name: "viewSetup notice, system notice and recent libraries",
  page: { storage: { "lm-recent": JSON.stringify(["/lib", "/other", "", 3, "/third"]) } },
  async run(env) {
    env.set("S.notice", { kind: "bad", title: "It failed", details: ["a", "b"] });
    await show(env, {
      mode: "advanced",
      task: "dedupe",
      f: { ...FULL, library: "/lib" },
      sys: { os: "linux", toolsOK: false, minVersion: "5.1", installHint: "Install it", tools: [{ name: "ffmpeg", found: false, problem: "not on PATH" }, { name: "ffprobe", found: true, ok: true }] },
    });
    return env.snap(env.app());
  },
});

cases.push({
  name: "viewSetup organize advanced one tier shows Folders title",
  async run(env) {
    await show(env, { mode: "advanced", task: "organize", f: { ...FULL, tiers: [["/a"]] } });
    return env.snap(env.find(env.app(), (e) => e.className === "tier"));
  },
});

cases.push({
  name: "viewSetup add easy empty tiers shows one input",
  async run(env) {
    await show(env, { task: "add", f: { ...FULL, tiers: [] } });
    const sec = env.find(env.app(), (e) => e.tagName === "section");
    return env.snap(sec);
  },
});

cases.push({
  name: "viewSetup radios easy and advanced order",
  async run(env) {
    const radios = () => env.app().all().filter((e) => e.tagName === "input" && e.attrs.type === "radio").map((e) => [e.value, e.checked]);
    await show(env, { task: "check" });
    const easy = radios();
    await show(env, { mode: "advanced", task: "export" });
    return { easy, advanced: radios() };
  },
});

cases.push({
  name: "viewSetup history box advanced with history",
  async run(env) {
    env.set("S.history", { library: "/lib", outDir: "/out", runs: [{ id: "r1", time: "2026-11-20T10:00:00Z", moves: 3, next: true }, { id: "r2", moves: 0 }, { id: "r3", moves: 1, undone: true }] });
    await show(env, { mode: "advanced", task: "dedupe" });
    return env.snap(env.byId("history"));
  },
});

// ---------------------------------------------------------------- organizeRequest

for (const task of [...TASK_KEYS, "bogus"]) {
  cases.push({
    name: `organizeRequest ${task}`,
    async run(env) {
      assign(env, FULL);
      env.set("S.task", task);
      const out = {};
      for (const mode of ["easy", "advanced"]) {
        env.set("S.mode", mode);
        out[mode] = { plain: env.run("JSON.stringify(organizeRequest())"), withTidy: env.run("JSON.stringify(organizeRequest(true))"), tidyKey: env.run("'tidy' in organizeRequest()") };
      }
      return out;
    },
  });
}

cases.push({
  name: "organizeRequest dedupe withTidy without ffmpeg drops media",
  async run(env) {
    assign(env, { ...FULL, tidy: { utf8: false, audio: true, video: true, dedupe: true } });
    env.set("S.task", "dedupe");
    env.set("S.sys", { toolsOK: false, tools: [] });
    const off = env.run("JSON.stringify(organizeRequest(true))");
    env.set("S.sys", null);
    return { off, noSys: env.run("JSON.stringify(organizeRequest(true))") };
  },
});

// ---------------------------------------------------------------- validate

for (const task of [...TASK_KEYS, "bogus", "constructor"]) {
  cases.push({
    name: `validate ${task} url and body`,
    async run(env) {
      const out = {};
      for (const mode of ["easy", "advanced"]) {
        const mark = env.requests.length;
        assign(env, FULL);
        env.set("S.task", task);
        env.set("S.mode", mode);
        env.run("void validate()");
        await env.flush();
        out[mode] = since(env, mark);
      }
      return out;
    },
  });
}

cases.push({
  name: "validate empty library posts nothing and clears",
  async run(env) {
    await show(env, { task: "dedupe" });
    env.set("S.resolved", { outDir: "/o", library: "/lib" });
    env.set("S.resolveError", "old");
    env.set("S.history", { library: "/lib", runs: [] });
    assign(env, { library: "   " });
    const mark = env.requests.length;
    env.run("void validate()");
    await env.flush();
    return { requests: since(env, mark), ...env.state(["resolved", "resolveError", "history"]), resolved: resolvedText(env) };
  },
});

cases.push({
  name: "validate answer sets resolved, easy mode loads no history",
  async run(env) {
    await show(env, { task: "add" });
    env.answers["/api/organize/validate"] = () => env.reply(200, { library: "/lib", outDir: "/lib-out" });
    const mark = env.requests.length;
    env.set("S.resolveError", "old");
    env.run("void validate()");
    await env.flush();
    return { requests: since(env, mark), state: env.state(["resolved", "resolveError", "history"]), shown: resolvedText(env) };
  },
});

cases.push({
  name: "validate answer advanced loads history",
  async run(env) {
    await show(env, { mode: "advanced", task: "check" });
    env.answers["/api/check/validate"] = () => env.reply(200, { library: "/lib", outDir: "/lib-out" });
    env.answers["/api/organize/history"] = () => env.reply(200, { outDir: "/lib-out", runs: [{ id: "r1", moves: 2, next: true }] });
    env.set("S.history", { library: "/other", runs: [] });
    const mark = env.requests.length;
    env.run("void validate()");
    await env.flush();
    return { requests: since(env, mark), state: env.state(["resolved", "history"]), history: env.snap(env.byId("history")), shown: resolvedText(env) };
  },
});

cases.push({
  name: "validate error sets resolveError",
  async run(env) {
    await show(env, { task: "remove" });
    env.answers["/api/remove/validate"] = () => env.reply(400, { error: "no such folder" });
    env.set("S.resolved", { outDir: "/o" });
    env.set("S.history", { library: "/lib", runs: [] });
    env.run("void validate()");
    await env.flush();
    return { state: env.state(["resolved", "resolveError", "history"]), shown: resolvedText(env) };
  },
});

// queued returns a fetch answer whose replies are queued: each call gets the next entry, a
// function (reply now) or a deferred (reply when the test resolves it).
function deferred() {
  let resolve;
  const promise = new Promise((r) => (resolve = r));
  return { promise, resolve };
}
function queued(list) {
  return () => {
    const next = list.shift();
    return typeof next === "function" ? next() : next.promise;
  };
}

cases.push({
  name: "validate stale success answer is dropped",
  async run(env) {
    await show(env, { task: "add", f: { ...FULL, library: "/old" } });
    const first = deferred();
    env.answers["/api/organize/validate"] = queued([first, () => env.reply(200, { library: "/new", outDir: "/new-out" })]);
    env.run("void validate()");
    await env.flush();
    assign(env, { library: "/new" });
    env.run("void validate()");
    await env.flush();
    const afterSecond = env.state(["resolved", "resolveError"]);
    first.resolve(env.reply(200, { library: "/old", outDir: "/old-out" }));
    await env.flush();
    return { afterSecond, state: env.state(["resolved", "resolveError"]), shown: resolvedText(env) };
  },
});

cases.push({
  name: "validate stale error answer is dropped",
  async run(env) {
    await show(env, { task: "add", f: { ...FULL, library: "/old" } });
    const first = deferred();
    env.answers["/api/organize/validate"] = queued([first, () => env.reply(200, { library: "/new", outDir: "/new-out" })]);
    env.run("void validate()");
    await env.flush();
    assign(env, { library: "/new" });
    env.run("void validate()");
    await env.flush();
    first.resolve(env.reply(400, { error: "no such folder" }));
    await env.flush();
    return { state: env.state(["resolved", "resolveError", "history"]), shown: resolvedText(env) };
  },
});

cases.push({
  name: "loadHistory stale history answer is dropped",
  async run(env) {
    await show(env, { mode: "advanced", task: "dedupe", f: { ...FULL, library: "/a" } });
    const hist1 = deferred();
    env.answers["/api/organize/validate"] = queued([
      () => env.reply(200, { library: "/a", outDir: "/a-out" }),
      () => env.reply(200, { library: "/b", outDir: "/b-out" }),
    ]);
    env.answers["/api/organize/history"] = queued([hist1, () => env.reply(200, { outDir: "/b-out", runs: [{ id: "b1", moves: 1, next: true }] })]);
    env.run("void validate()");
    await env.flush();
    assign(env, { library: "/b" });
    env.run("void validate()");
    await env.flush();
    hist1.resolve(env.reply(200, { outDir: "/a-out", runs: [{ id: "a1", moves: 5, next: true }] }));
    await env.flush();
    return { state: env.state(["resolved", "history"]), history: env.snap(env.byId("history")) };
  },
});

cases.push({
  name: "validate clears another library's history easy mode",
  async run(env) {
    await show(env, { task: "add" });
    env.answers["/api/organize/validate"] = () => env.reply(200, { library: "/lib", outDir: "/lib-out" });
    env.set("S.history", { library: "/other", runs: [] });
    env.run("void validate()");
    await env.flush();
    const other = env.state(["history"]);
    env.set("S.history", { library: "/lib", runs: [{ id: "keep", moves: 1 }] });
    env.run("void validate()");
    await env.flush();
    return { other, same: env.state(["history"]) };
  },
});

cases.push({
  name: "validate advanced shows no list while the new library's history loads",
  async run(env) {
    env.set("S.history", { library: "/other", outDir: "/o", runs: [{ id: "r1", moves: 3, next: true }] });
    await show(env, { mode: "advanced", task: "dedupe", f: { ...FULL, library: "/lib" } });
    const hist = deferred();
    env.answers["/api/organize/validate"] = () => env.reply(200, { library: "/lib", outDir: "/lib-out" });
    env.answers["/api/organize/history"] = () => hist.promise;
    const before = env.snap(env.byId("history"));
    env.run("void validate()");
    await env.flush();
    const loading = { state: env.state(["history"]), history: env.snap(env.byId("history")) };
    hist.resolve(env.reply(200, { outDir: "/lib-out", runs: [] }));
    await env.flush();
    return { before, loading, after: env.state(["history"]) };
  },
});

// ---------------------------------------------------------------- updateResolved

for (const task of [...TASK_KEYS, "bogus"]) {
  cases.push({
    name: `updateResolved ${task}`,
    async run(env) {
      await show(env, { task, mode: task === "export" ? "advanced" : "easy" });
      const out = {};
      out.neither = resolvedText(env);
      env.set("S.resolved", { library: "/lib", outDir: "/the/out" });
      env.run("updateResolved()");
      out.resolved = resolvedText(env);
      env.set("S.resolveError", "Bad folder");
      env.run("updateResolved()");
      out.errorAndResolved = resolvedText(env);
      env.set("S.resolved", null);
      env.run("updateResolved()");
      out.error = resolvedText(env);
      return out;
    },
  });
}

cases.push({
  name: "updateResolved remove blank patterns",
  async run(env) {
    await show(env, { task: "remove", f: { ...FULL, patterns: " \n\t" } });
    const out = { neither: resolvedText(env) };
    env.set("S.resolved", { outDir: "/o" });
    env.run("updateResolved()");
    out.resolved = resolvedText(env);
    env.set("S.resolveError", "Bad");
    env.run("updateResolved()");
    out.error = resolvedText(env);
    return out;
  },
});

cases.push({
  name: "updateResolved remove typing patterns enables start without render",
  async run(env) {
    await show(env, { task: "remove", f: { ...FULL, patterns: "" } });
    env.set("S.resolved", { outDir: "/o" });
    env.run("updateResolved()");
    const area = env.byId("patterns");
    const before = resolvedText(env);
    const timers0 = env.timers.length;
    input(env, area, "Queen");
    const typed = resolvedText(env);
    input(env, area, "   ");
    const blanked = resolvedText(env);
    return {
      before, typed, blanked,
      sameArea: env.byId("patterns") === area,
      patterns: env.state(["f"]).f.patterns,
      stored: JSON.parse(env.store.get("lm-form")).patterns,
      timers: env.timers.slice(timers0).map((t) => t.ms),
    };
  },
});

cases.push({
  name: "updateResolved without the page elements does nothing",
  async run(env) {
    env.set("S.view", "done");
    env.set("S.task", "remove");
    env.set("S.resolved", { outDir: "/o" });
    env.run("updateResolved()");
    return { resolved: env.byId("resolved"), start: env.byId("start") };
  },
});

for (const gone of ["start", "resolved"]) {
  cases.push({
    name: `updateResolved without #${gone} does nothing`,
    async run(env) {
      await show(env, { task: "dedupe" });
      const kept = env.byId(gone === "start" ? "resolved" : "start");
      env.byId(gone).remove();
      const before = env.snap(kept);
      env.set("S.resolved", { library: "/lib", outDir: "/o" });
      let error = null;
      try {
        env.run("updateResolved()");
      } catch (e) {
        error = String(e.message);
      }
      return { error, gone: env.byId(gone), same: JSON.stringify(env.snap(kept)) === JSON.stringify(before), kept: env.snap(kept) };
    },
  });
}

// ---------------------------------------------------------------- start buttons

for (const task of TASK_KEYS) {
  cases.push({
    name: `start button ${task} builds the request at click time`,
    async run(env) {
      await show(env, { task, mode: "advanced" });
      env.set("S.notice", { kind: "bad", title: "old" });
      const lib = libraryInput(env);
      if (lib) input(env, lib, " /typed ");
      const mark = env.requests.length;
      const btn = startBtn(env);
      const label = btn.textContent;
      env.scrolls.length = 0;
      await env.click(btn);
      return {
        label,
        requests: since(env, mark),
        state: env.state(["view", "notice", "asideFrom", "reportsFrom"]),
        startNow: startBtn(env) && { text: startBtn(env).textContent, disabled: startBtn(env).disabled },
        scrolls: env.scrolls,
        title: env.app().all().find((e) => e.tagName === "h1")?.textContent,
      };
    },
  });
}

cases.push({
  name: "start button organize easy typed tier",
  async run(env) {
    await show(env, { task: "organize", f: { ...FULL, tiers: [["/a"]] } });
    const tier = env.find(env.app(), (e) => e.tagName === "input" && e.attrs["aria-label"] === "Folders to take songs from 1");
    input(env, tier, " /typed-tier ");
    const mark = env.requests.length;
    await env.click(startBtn(env));
    return since(env, mark);
  },
});

cases.push({
  name: "start button remove typed patterns",
  async run(env) {
    await show(env, { task: "remove" });
    input(env, env.byId("patterns"), " Queen - X ");
    const mark = env.requests.length;
    await env.click(startBtn(env));
    return since(env, mark);
  },
});

cases.push({
  name: "start button restore answered shows the aside view",
  async run(env) {
    await show(env, { task: "restore" });
    env.answers["/api/setaside/list"] = () => env.reply(200, { config: { library: "/lib", outDir: "/out" }, items: [] });
    env.set("S.pick", env.run("new Set(['x'])"));
    await env.click(startBtn(env));
    return { state: env.state(["view", "asideFrom", "pick", "notice"]), recent: env.store.get("lm-recent"), scrolls: env.scrolls };
  },
});

cases.push({
  name: "start button restore sets asideFrom and reportsFrom stays",
  async run(env) {
    await show(env, { task: "restore" });
    env.set("S.asideFrom", "overview");
    env.set("S.reportsFrom", "done");
    await env.click(startBtn(env));
    return env.state(["asideFrom", "reportsFrom", "view"]);
  },
});

cases.push({
  name: "start button reports sets reportsFrom",
  async run(env) {
    await show(env, { task: "reports" });
    env.set("S.reportsFrom", "done");
    env.set("S.asideFrom", "overview");
    await env.click(startBtn(env));
    return env.state(["asideFrom", "reportsFrom", "view"]);
  },
});

cases.push({
  name: "start button restore failing shows the notice on setup",
  async run(env) {
    await show(env, { task: "restore" });
    env.answers["/api/setaside/list"] = () => env.reply(404, { error: "nothing there", details: ["d"] });
    await env.click(startBtn(env));
    return { state: env.state(["view", "asideFrom", "notice"]), start: startBtn(env) && startBtn(env).textContent };
  },
});

cases.push({
  name: "start button overview answered starts the job",
  async run(env) {
    await show(env, { task: "overview" });
    env.answers["/api/overview"] = () => env.reply(202, { id: "j1", kind: "overview", state: "running" });
    await env.click(startBtn(env));
    return { state: env.state(["view", "waitingFor", "notice"]), recent: env.store.get("lm-recent") };
  },
});

cases.push({
  name: "start button export failing shows the notice",
  async run(env) {
    await show(env, { task: "export", mode: "advanced" });
    env.answers["/api/export"] = () => env.reply(409, { error: "a job is running" });
    await env.click(startBtn(env));
    return { state: env.state(["view", "notice"]), notice: env.snap(env.find(env.app(), (e) => e.className.startsWith("notice"))) };
  },
});

cases.push({
  name: "start button dedupe options page",
  async run(env) {
    await show(env, { task: "dedupe" });
    env.set("S.notice", { title: "x" });
    const mark = env.requests.length;
    env.scrolls.length = 0;
    await env.click(startBtn(env));
    return { requests: since(env, mark), state: env.state(["view", "notice"]), scrolls: env.scrolls, title: env.app().all().find((e) => e.tagName === "h1").textContent };
  },
});

// ---------------------------------------------------------------- startAnalysis

for (const task of ["dedupe", "add", "organize", "check", "remove", "bogus", "export", "restore", "overview"]) {
  cases.push({
    name: `startAnalysis ${task}`,
    async run(env) {
      assign(env, FULL);
      env.set("S.task", task);
      env.set("S.mode", "advanced");
      env.set("S.sys", SYS);
      const mark = env.requests.length;
      env.run("startAnalysis()");
      await env.flush();
      return since(env, mark);
    },
  });
}

cases.push({
  name: "startAnalysis from the tidy options page",
  async run(env) {
    await show(env, { task: "dedupe" });
    await env.click(startBtn(env));
    const mark = env.requests.length;
    await env.click(env.button(env.app(), "Check first"));
    return since(env, mark);
  },
});

// ---------------------------------------------------------------- task radios

for (const [from, to, mode] of [["dedupe", "remove", "easy"], ["check", "export", "advanced"], ["export", "organize", "advanced"], ["add", "add", "easy"]]) {
  cases.push({
    name: `task radio ${from} to ${to} ${mode}`,
    async run(env) {
      await show(env, { task: from, mode });
      env.set("S.notice", { title: "n" });
      env.set("S.resolved", { outDir: "/o" });
      env.set("S.resolveError", "e");
      env.set("S.history", { library: "/lib", runs: [] });
      const radio = env.find(env.app(), (e) => e.tagName === "input" && e.attrs.type === "radio" && e.value === to);
      const mark = env.requests.length;
      radio.dispatch("change");
      const sync = { state: env.state(["task", "notice", "resolved", "resolveError", "history", "view"]), stored: env.store.get("lm-task") };
      await env.flush();
      return {
        sync,
        requests: since(env, mark),
        checked: env.app().all().filter((e) => e.tagName === "input" && e.attrs.type === "radio" && e.checked).map((e) => e.value),
        start: startBtn(env).textContent,
        fields: env.find(env.app(), (e) => e.tagName === "section").all().filter((e) => e.className === "label").map((e) => e.textContent),
      };
    },
  });
}

cases.push({
  name: "task radio with empty library posts nothing",
  async run(env) {
    await show(env, { task: "dedupe", f: { ...FULL, library: "" } });
    const radio = env.find(env.app(), (e) => e.tagName === "input" && e.attrs.type === "radio" && e.value === "check");
    const mark = env.requests.length;
    radio.dispatch("change");
    await env.flush();
    return { requests: since(env, mark), state: env.state(["task"]), shown: resolvedText(env) };
  },
});

// ---------------------------------------------------------------- startup task choice

const startup = [
  ["no storage", {}],
  ["check", { "lm-task": "check" }],
  ["unknown", { "lm-task": "nope" }],
  ["constructor", { "lm-task": "constructor" }],
  ["__proto__", { "lm-task": "__proto__" }],
  ["toString", { "lm-task": "toString" }],
  ["export easy", { "lm-task": "export" }],
  ["export advanced", { "lm-task": "export", "lm-mode": "advanced" }],
  ["export mode Advanced capitalised", { "lm-task": "export", "lm-mode": "Advanced" }],
  ["organize advanced", { "lm-task": "organize", "lm-mode": "advanced" }],
];
for (const [label, storage] of startup) {
  cases.push({
    name: `startup task ${label}`,
    page: { storage },
    async run(env) {
      const first = { ...env.state(["task", "mode", "view"]), requests: env.requests.slice() };
      env.set("S.sys", SYS);
      env.run("render()");
      await env.flush();
      return { ...first, h1: env.app().all().find((e) => e.tagName === "h1")?.textContent, start: startBtn(env)?.textContent, fields: env.app().all().filter((e) => e.className === "label").map((e) => e.textContent) };
    },
  });
}

cases.push({
  name: "startup saved form loads into the setup page",
  page: { storage: { "lm-task": "add", "lm-form": JSON.stringify({ library: "/saved", tiers: [["/s1"]], tidy: { video: true } }) } },
  async run(env) {
    env.set("S.sys", SYS);
    env.run("render()");
    await env.flush();
    return { f: env.state(["f"]).f, values: env.app().all().filter((e) => e.tagName === "input" && e.attrs.type === "text").map((e) => e.value) };
  },
});

module.exports = cases;
