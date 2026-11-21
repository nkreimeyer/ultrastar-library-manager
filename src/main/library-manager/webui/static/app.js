// library-manager UI. Plain DOM, no build step. Every text goes in as a text node.

const nf = new Intl.NumberFormat();
const fmt = (n) => nf.format(n || 0);
const plural = (n, one, many) => `${fmt(n)} ${n === 1 ? one : many}`;

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else if (k === "checked") el.checked = v;
    else if (k === "style") for (const [p, val] of Object.entries(v)) el.style.setProperty(p, val);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
  return el;
}

// add appends the kids that are not null or false (Node.append would write "null").
function add(el, ...kids) {
  for (const k of kids.flat(Infinity)) if (k != null && k !== false && k !== "") el.append(k);
  return el;
}

class ApiError extends Error {
  constructor(msg, status, details) {
    super(msg);
    this.status = status;
    this.details = details;
  }
}

async function call(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { error: text.trim() };
  }
  if (!res.ok) throw new ApiError(data?.error || res.statusText, res.status, data?.details);
  return data;
}
const get = (p) => call("GET", p);
const post = (p, b) => call("POST", p, b ?? {});

// ---------------------------------------------------------------- state

const TASKS = {
  dedupe: {
    title: "Tidy up my library",
    desc: "Fix song texts, set aside duplicates, broken songs and leftover files, and even out the volume. You choose what on the next page.",
  },
  add: {
    title: "Add new songs to my library",
    desc: "Bring songs from other folders into the library. Songs it already has are compared, and only the best version is kept.",
  },
  reports: {
    title: "Read the reports of earlier runs",
    desc: "Open the lists the tools wrote: what was planned or moved, songs with encoding problems, missing files and check results.",
  },
  restore: {
    title: "Put back set-aside songs",
    desc: "Look at the duplicates, broken songs, removed songs and leftover files that were set aside, and put back the ones you want to keep.",
  },
  check: {
    title: "Check my songs for problems",
    desc: "Find songs that cannot be played or have problems, such as missing audio, damaged files or notes that do not fit the music. Nothing is changed.",
  },
  remove: {
    title: "Remove songs from my library",
    desc: "Set aside every song of the artists or songs you name. Nothing is deleted: the songs are moved out of the library and can be put back.",
  },
  organize: {
    title: "Build a new library from folders",
    desc: "Sort songs from one or more folders into a new library with a folder per first letter (A to Z, 0-9 and #).",
  },
};

const PHASES = {
  text: "Fixing song texts",
  cleanup: "Clearing out leftovers",
  inspect: "Checking songs",
  move: "Moving songs",
  video: "Converting videos",
  audio: "Evening out the volume",
  undo: "Moving songs back",
  restore: "Putting songs back",
};

// The tidy-up options, in groups. Every option goes to the server as it is (tidyOptions).
// media: needs ffmpeg; off when it is missing.
const TIDY_GROUPS = [
  { title: "Song texts", options: [
    { key: "utf8", label: "Convert all song texts to UTF-8",
      hint: "Umlauts and other special letters then show up right in every karaoke program. A text whose encoding is not certain is left alone and listed in the report." },
    { key: "fileTags", label: "Fix the file names in the song texts",
      hint: "Points #MP3, #VIDEO, #COVER and #BACKGROUND to the files that are really in the song folder. Song texts in an older encoding are only fixed together with the UTF-8 conversion." },
    { key: "fillTags", label: "Fill in a missing artist or title",
      hint: "Taken from a song folder named “Artist - Title”. Karaoke programs skip songs without them. Like the file names, this needs the UTF-8 conversion for song texts in an older encoding." },
  ] },
  { title: "Clean up", options: [
    { key: "dedupe", label: "Set aside duplicates",
      hint: "Finds songs that are in the library more than once and keeps the best version." },
    { key: "broken", label: "Set aside broken songs",
      hint: "Songs that cannot be played, such as songs without audio or with broken notes, and folders without a song. Each song is listed for you to look at first." },
    { key: "leftovers", label: "Set aside leftover files",
      hint: "Files no song uses, such as old covers or a second song text." },
  ] },
  { title: "Audio and video", media: true, options: [
    { key: "audio", label: "Even out the volume",
      hint: "Brings every song to the same peak level and mixes surround sound down to stereo. The original audio files are kept in the output folder." },
    { key: "video", label: "Convert videos",
      hint: "Re-encodes large videos and old formats to H.264 (at most 1080p) and removes audio tracks the song does not use. Saves a lot of space, but takes long and cannot be undone." },
  ] },
];

const TIDY_DEFAULTS = { utf8: true, fileTags: true, fillTags: true, dedupe: true, broken: true, leftovers: true, audio: true, video: false };

// What the steps of a finished run did, from their stats (a step's key is its name).
const STEP_DONE = {
  text: (st) => [st.written && plural(st.written, "song text fixed", "song texts fixed"),
    st.moved && plural(st.moved, "file or folder set aside", "files and folders set aside")],
  cleanup: (st) => [st.moved && plural(st.moved, "leftover file or folder set aside", "leftover files and folders set aside")],
  songs: (st) => [st.moved && plural(st.moved, "song folder set aside", "song folders set aside")],
  // A conversion to another container is a rename (moved), one in place a replace.
  video: (st) => [st.replaced + st.moved && plural(st.replaced + st.moved, "video converted", "videos converted")],
  audio: (st) => [st.replaced && plural(st.replaced, "audio file evened out", "audio files evened out")],
};

// The phases of a check (a dry run) where the real run's words would alarm.
const DRY_PHASES = { text: "Checking song texts", cleanup: "Looking for leftovers" };

// What undo covers in a tidy-up: only the song step journals its moves.
const TIDY_UNDO = "Duplicates and broken songs can be put back with undo afterwards. Leftover files and folders without a song are not part of undo: they wait in the extra-files and invalid folders of the output folder. Fixed song texts, evened-out audio and converted videos stay as they are.";

const isTidyJob = (j) => !!j?.kind?.startsWith("tidy-");

const JOB_TITLES = {
  "organize-analyse": "Checking first. Nothing is changed yet.",
  "organize-apply": "Moving songs",
  "organize-undo": "Undoing the last run",
  "setaside-restore": "Putting songs back",
  "check-analyse": "Checking the songs. Nothing is changed.",
  "check-apply": "Checking and setting aside broken songs",
  "remove-analyse": "Checking first. Nothing is changed yet.",
  "remove-apply": "Removing songs",
};

// What the Stop button does to a job that changes files (the others change nothing).
const STOP_HINTS = {
  "organize-apply": "Stopping finishes the current move; undo puts back what was moved.",
  "organize-undo": "Stopping finishes the current move; undo again to move back the rest.",
  "remove-apply": "Stopping finishes the current move; undo puts back what was moved.",
  "setaside-restore": "Stopping finishes the current move; undo takes back what was put back.",
  "check-apply": "Stopping finishes the current move. Nothing is moved if you stop before the check is done.",
};

const emptyForm = () => ({
  library: "",
  tiers: [[""]],
  out: "",
  patterns: "",
  priority: false,
  ignoreThe: false,
  deep: false,
  tidy: { ...TIDY_DEFAULTS },
});

function loadForm() {
  try {
    const saved = JSON.parse(localStorage.getItem("lm-form") || "{}");
    return { ...emptyForm(), ...saved, tidy: { ...TIDY_DEFAULTS, ...saved.tidy } };
  } catch {
    return emptyForm();
  }
}

const S = {
  mode: localStorage.getItem("lm-mode") === "advanced" ? "advanced" : "easy",
  task: TASKS[localStorage.getItem("lm-task")] ? localStorage.getItem("lm-task") : "dedupe",
  f: loadForm(),
  sys: null,
  view: "setup", // setup | options (tidy-up) | running | review | done
  job: null, // latest job state from the server
  waitingFor: null, // id of the job this page started or follows
  handled: new Set(),
  org: null, // organize review view
  rem: null, // remove review view
  chk: null, // check report view
  reports: null, // report viewer: { config, files, shown: { name, text, truncated } }
  aside: null, // set-aside browser: { config, items }
  pick: new Set(), // keys (section/rel) of the items chosen to put back
  open: new Set(), // open review sections
  notice: null,
  done: null,
  resolved: null,
  resolveError: null,
  history: null,
  logs: [],
  logJob: null,
};

const advanced = () => S.mode === "advanced";
const saveForm = () => localStorage.setItem("lm-form", JSON.stringify(S.f));

// ---------------------------------------------------------------- rendering

const app = document.getElementById("app");

function render() {
  app.classList.toggle("wide", advanced() && S.view === "review" && !!S.org && !S.rem && !S.chk);
  let content;
  switch (S.view) {
    case "running":
      content = viewRunning();
      break;
    case "review":
      content = S.chk ? viewCheckReview() : S.rem ? viewRemoveReview() : viewOrganizeReview();
      break;
    case "done":
      content = viewDone();
      break;
    case "options":
      content = viewOptions();
      break;
    case "aside":
      content = viewAside();
      break;
    case "reports":
      content = viewReports();
      break;
    default:
      content = viewSetup();
  }
  app.replaceChildren(content);
  document.getElementById("log").hidden = !advanced();
  for (const b of document.querySelectorAll(".switch button")) {
    b.setAttribute("aria-pressed", String(b.dataset.mode === S.mode));
  }
}

function noticeBox(n) {
  if (!n) return null;
  return h(
    "div",
    { class: `notice ${n.kind || ""}`, role: n.kind === "bad" ? "alert" : "status" },
    n.title ? h("p", null, h("strong", null, n.title)) : null,
    n.text ? h("p", null, n.text) : null,
    n.details?.length ? h("ul", null, n.details.map((d) => h("li", { class: "small path" }, d))) : null,
  );
}

// ---------------------------------------------------------------- system check

function toolsPill() {
  const el = document.getElementById("tools");
  if (!S.sys) return;
  const ff = S.sys.tools.find((t) => t.name === "ffmpeg");
  const unknown = S.sys.tools.some((t) => t.ok && !t.known);
  let cls = "ok";
  let text = `ffmpeg ${ff?.version || ""}`;
  if (!S.sys.toolsOK) {
    cls = S.sys.tools.some((t) => !t.found) ? "bad" : "warn";
    text = S.sys.tools.some((t) => !t.found) ? "ffmpeg missing" : "ffmpeg too old";
  } else if (unknown) {
    cls = "warn";
  }
  el.replaceChildren(h("span", { class: `dot ${cls}`, "aria-hidden": "true" }), text);
  el.title = S.sys.tools.map((t) => `${t.name}: ${t.found ? t.path + " " + (t.version || "") : "not found"}`).join("\n");
}

function systemNotice() {
  if (!S.sys || S.sys.toolsOK) return null;
  const missing = S.sys.tools.filter((t) => !t.found).map((t) => t.name);
  const title = missing.length
    ? `${missing.join(" and ")} ${missing.length === 1 ? "is" : "are"} not installed`
    : `ffmpeg is older than ${S.sys.minVersion}`;
  return h(
    "div",
    { class: "notice" },
    h("p", null, h("strong", null, title)),
    h(
      "p",
      null,
      "Without it, video and audio quality are not compared when the best version of a song is picked, and the volume and videos cannot be worked on. Everything else works without it.",
    ),
    h("p", { class: "small" }, S.sys.installHint),
    advanced() ? h("ul", null, S.sys.tools.filter((t) => t.problem).map((t) => h("li", { class: "small" }, `${t.name}: ${t.problem}`))) : null,
    h("p", null, h("button", { class: "btn", type: "button", onclick: loadSystem }, "Check again")),
  );
}

async function loadSystem() {
  try {
    S.sys = await get("/api/system");
    toolsPill();
    if (S.view === "setup") render();
  } catch (e) {
    if (e.status === 403) {
      app.replaceChildren(
        h("h1", null, "Open the link from the terminal"),
        h("p", null, "This page only works through the address library-manager printed when it started. The address works once: if this browser lost its sign-in (it was closed, or another browser used the address first), restart library-manager and open the new address."),
      );
      return false;
    }
    document.getElementById("tools").textContent = "ffmpeg check failed";
  }
  return true;
}

// ---------------------------------------------------------------- folder fields

function folderInput(value, onChange, label) {
  const input = h("input", {
    type: "text",
    value,
    placeholder: "Pick or paste a folder",
    "aria-label": label,
    spellcheck: "false",
    oninput: (e) => onChange(e.target.value),
  });
  const browse = h("button", {
    class: "btn",
    type: "button",
    onclick: () =>
      pickFolder(input.value, (p) => {
        input.value = p;
        onChange(p);
      }),
  }, "Browse…");
  return h("div", { class: "row" }, input, browse);
}

// The libraries used lately, newest first (in this browser only).
const RECENT_MAX = 6;
function recentLibraries() {
  try {
    const list = JSON.parse(localStorage.getItem("lm-recent") || "[]");
    return Array.isArray(list) ? list.filter((p) => typeof p === "string" && p) : [];
  } catch {
    return [];
  }
}

function rememberLibrary(path) {
  path = (path || "").trim();
  if (!path) return;
  try {
    localStorage.setItem("lm-recent", JSON.stringify([path, ...recentLibraries().filter((p) => p !== path)].slice(0, RECENT_MAX)));
  } catch {
    // storage full or blocked: the list is a convenience
  }
}

function recentField(key) {
  const recent = recentLibraries().filter((p) => p !== S.f[key].trim());
  if (!recent.length) return null;
  return h("div", { class: "recent", "aria-label": "Recent libraries" }, h("span", { class: "muted small" }, "Recent: "),
    recent.map((p) => h("button", { class: "link path", type: "button", title: p, onclick: () => {
      S.f[key] = p;
      saveForm();
      render();
      validate();
    } }, p)));
}

function folderField(label, key, hint) {
  return h(
    "div",
    { class: "field" },
    h("div", { class: "label" }, label),
    folderInput(S.f[key], (v) => setField(key, v), label),
    key === "library" ? recentField(key) : null,
    hint ? h("p", { class: "hint" }, hint) : null,
  );
}

function setField(key, v) {
  S.f[key] = v;
  saveForm();
  validateSoon();
}

// tiersField edits S.f.tiers. Easy mode shows one list (one tier); advanced mode shows the
// priority groups.
function tiersField(label, hint) {
  const wrap = h("div", { class: "field" }, h("div", { class: "label" }, label));
  const rerender = () => {
    saveForm();
    validateSoon();
    render();
  };
  if (!advanced()) {
    const flat = S.f.tiers.flat();
    const list = flat.length ? flat : [""];
    list.forEach((p, i) => {
      const row = folderInput(p, (v) => {
        list[i] = v;
        S.f.tiers = [list];
        saveForm();
        validateSoon();
      }, `${label} ${i + 1}`);
      if (list.length > 1) {
        row.append(h("button", { class: "btn", type: "button", "aria-label": "Remove this folder", onclick: () => {
          list.splice(i, 1);
          S.f.tiers = [list];
          rerender();
        } }, "Remove"));
      }
      wrap.append(row, h("div", { style: { height: "0.4rem" } }));
    });
    wrap.append(h("button", { class: "link", type: "button", onclick: () => {
      S.f.tiers = [[...list, ""]];
      rerender();
    } }, "Add another folder"));
    if (hint) wrap.append(h("p", { class: "hint" }, hint));
    return wrap;
  }
  S.f.tiers.forEach((tier, ti) => {
    const box = h("div", { class: "tier" }, h("div", { class: "tier-title" }, S.f.tiers.length > 1 ? `Priority group ${ti + 1}` : "Folders"));
    tier.forEach((p, i) => {
      const row = folderInput(p, (v) => {
        tier[i] = v;
        saveForm();
        validateSoon();
      }, `Group ${ti + 1}, folder ${i + 1}`);
      row.append(h("button", { class: "btn", type: "button", "aria-label": "Remove this folder", onclick: () => {
        tier.splice(i, 1);
        if (!tier.length) S.f.tiers.splice(ti, 1);
        if (!S.f.tiers.length) S.f.tiers = [[""]];
        rerender();
      } }, "Remove"));
      box.append(row);
    });
    box.append(h("button", { class: "link", type: "button", onclick: () => {
      tier.push("");
      rerender();
    } }, "Add a folder to this group"));
    wrap.append(box);
  });
  wrap.append(h("button", { class: "link", type: "button", onclick: () => {
    S.f.tiers.push([""]);
    rerender();
  } }, "Add a lower-priority group"));
  wrap.append(h("p", { class: "hint" }, "Earlier groups win ties. With “Priority decides first”, a song from an earlier group always wins over a later one."));
  return wrap;
}

function checkField(label, key, hint) {
  return h("label", { class: "check" },
    h("input", { type: "checkbox", checked: S.f[key], onchange: (e) => setField(key, e.target.checked) }),
    h("span", null, label, hint ? h("span", { class: "hint small muted" }, h("br"), hint) : null),
  );
}

// ---------------------------------------------------------------- folder picker

async function pickFolder(start, onPick) {
  const dlg = document.getElementById("picker");
  let hidden = false;
  const show = async (path) => {
    let l;
    try {
      l = await get(`/api/fs/list?path=${encodeURIComponent(path || "")}${hidden ? "&hidden=1" : ""}`);
    } catch (e) {
      l = { path, dirs: [], roots: [], error: e.message };
    }
    const list = h("ul", { class: "picker-list" });
    if (l.parent) list.append(h("li", null, h("button", { type: "button", onclick: () => show(l.parent) }, "↑ Up one level")));
    for (const d of l.dirs) list.append(h("li", null, h("button", { type: "button", onclick: () => show(d.path) }, d.name)));
    if (!l.dirs.length) list.append(h("li", { class: "empty" }, l.error ? `Cannot open this folder: ${l.error}` : "No folders in here."));
    dlg.replaceChildren(h("form", { method: "dialog", class: "dlg" },
      h("h2", null, "Pick a folder"),
      h("div", { class: "picker-roots" }, l.roots.map((r) => h("button", { class: "btn", type: "button", onclick: () => show(r.path) }, r.name))),
      h("p", { class: "path" }, l.path),
      list,
      h("label", { class: "check" }, h("input", { type: "checkbox", checked: hidden, onchange: (e) => {
        hidden = e.target.checked;
        show(l.path);
      } }), h("span", { class: "small" }, "Show hidden folders")),
      h("div", { class: "actions" },
        h("button", { class: "btn primary", type: "button", disabled: !!l.error, onclick: () => {
          dlg.close();
          onPick(l.path);
        } }, "Use this folder"),
        h("button", { class: "btn", value: "cancel" }, "Cancel"),
      ),
    ));
    if (!dlg.open) dlg.showModal();
    list.querySelector("button")?.focus();
  };
  await show(start);
}

function confirmDialog(title, text, okLabel) {
  const dlg = document.getElementById("confirm");
  return new Promise((resolve) => {
    dlg.replaceChildren(h("form", { method: "dialog", class: "dlg" },
      h("h2", null, title),
      text.map((t) => h("p", null, t)),
      h("div", { class: "actions" },
        h("button", { class: "btn primary", value: "ok" }, okLabel),
        h("button", { class: "btn", value: "cancel", autofocus: true }, "Cancel"),
      ),
    ));
    dlg.returnValue = "";
    dlg.onclose = () => resolve(dlg.returnValue === "ok");
    dlg.showModal();
  });
}

// ---------------------------------------------------------------- setup

// mediaOK: the tools the audio and video options need are there.
const mediaOK = () => !S.sys || S.sys.toolsOK;

// tidyRequest returns the tidy-up options to send: the media options only with ffmpeg.
function tidyRequest() {
  const t = { ...S.f.tidy };
  if (!mediaOK()) t.audio = t.video = false;
  return t;
}

const anyTidy = (t) => Object.keys(TIDY_DEFAULTS).some((k) => t[k]);

// organizeRequest is the request of a check; withTidy adds the tidy-up options (a dedupe
// from the options page).
function organizeRequest(withTidy) {
  if (S.task === "remove") {
    return { library: S.f.library.trim(), out: advanced() ? S.f.out.trim() : "", patterns: S.f.patterns };
  }
  if (S.task === "check") {
    return { library: S.f.library.trim(), out: advanced() ? S.f.out.trim() : "", deep: advanced() && S.f.deep };
  }
  if (S.task === "restore" || S.task === "reports") return { library: S.f.library.trim(), out: advanced() ? S.f.out.trim() : "" };
  return {
    tidy: withTidy && S.task === "dedupe" ? tidyRequest() : undefined,
    mode: S.task,
    tiers: S.task === "dedupe" ? [] : S.f.tiers.map((t) => t.map((p) => p.trim()).filter(Boolean)).filter((t) => t.length),
    library: S.f.library.trim(),
    out: advanced() ? S.f.out.trim() : "",
    priority: advanced() && S.f.priority,
    ignoreThe: advanced() && S.f.ignoreThe,
    deep: advanced() && S.f.deep,
  };
}

let validateTimer = 0;
let validateSeq = 0;
function validateSoon() {
  clearTimeout(validateTimer);
  validateTimer = setTimeout(validate, 300);
}

async function validate() {
  const seq = ++validateSeq;
  if (!S.f.library.trim()) {
    S.resolved = null;
    S.resolveError = null;
    S.history = null;
    updateResolved();
    return;
  }
  try {
    const res = await post(`/api/${{ remove: "remove", check: "check", restore: "setaside", reports: "setaside" }[S.task] || "organize"}/validate`, organizeRequest());
    if (seq !== validateSeq) return;
    S.resolved = res;
    S.resolveError = null;
    if (advanced()) loadHistory();
  } catch (e) {
    if (seq !== validateSeq) return;
    S.resolved = null;
    S.resolveError = e.message;
    S.history = null;
  }
  updateResolved();
}

async function loadHistory() {
  try {
    S.history = await post("/api/organize/history", { library: S.resolved.library, out: S.resolved.outDir });
  } catch {
    S.history = null;
  }
  const box = document.getElementById("history");
  if (box) box.replaceChildren(historyList());
}

function updateResolved() {
  const el = document.getElementById("resolved");
  const btn = document.getElementById("start");
  if (!el || !btn) return;
  btn.disabled = !S.resolved || (S.task === "remove" && !S.f.patterns.trim());
  if (S.resolveError) {
    el.replaceChildren(h("span", { class: "error-text" }, S.resolveError));
  } else if (S.resolved) {
    const out = S.resolved.outDir;
    el.replaceChildren(
      S.task === "check" ? "The reports go to " : S.task === "restore" || S.task === "reports" ? "Looking in " : "Set-aside songs, reports and the record for undoing go to ",
      h("span", { class: "path" }, out),
      ".",
    );
  } else {
    el.replaceChildren("Pick the library folder to go on.");
  }
  if (S.task === "remove" && S.resolved && !S.resolveError && !S.f.patterns.trim()) el.append(" Write who or what to remove to go on.");
}

function viewSetup() {
  const frag = h("div");
  add(frag, h("h1", null, "What do you want to do?"));
  add(frag, noticeBox(S.notice), systemNotice() || "");
  const tasks = h("ul", { class: "tasks", role: "radiogroup", "aria-label": "Task" });
  for (const [key, t] of Object.entries(TASKS)) {
    tasks.append(h("li", null, h("label", null,
      h("input", { type: "radio", name: "task", value: key, checked: S.task === key, onchange: () => {
        S.task = key;
        localStorage.setItem("lm-task", key);
        S.notice = null;
        S.resolved = null;
        S.resolveError = null;
        render();
        validate();
      } }),
      h("span", { class: "t" }, t.title),
      h("span", { class: "d" }, t.desc),
    )));
  }
  add(frag, tasks);

  const fields = h("section", { "aria-label": "Folders and options" });
  switch (S.task) {
    case "dedupe":
      fields.append(folderField("Your library", "library", "The folder that holds your songs. What is set aside is moved out of it; the songs you keep stay where they are."));
      break;
    case "add":
      fields.append(folderField("Your library", "library", "The songs end up here, sorted into a folder per first letter."));
      fields.append(tiersField("Folders with new songs"));
      break;
    case "check":
      fields.append(folderField("Your library", "library", "The folder that holds your songs. It is only read, never changed."));
      break;
    case "restore":
      fields.append(folderField("Your library", "library", "The library the songs were set aside from. Its output folder is where they are now."));
      break;
    case "reports":
      fields.append(folderField("Your library", "library", "The reports are in its output folder."));
      break;
    case "remove":
      fields.append(folderField("Your library", "library", "The folder that holds your songs. The songs you name are moved out of it."));
      fields.append(patternsField());
      break;
    case "organize":
      fields.append(tiersField("Folders to take songs from", "The songs are moved out of these folders."));
      fields.append(folderField("New library folder", "library", "Can be a new, empty folder."));
      break;
  }
  if (advanced()) {
    const opts = h("section", { "aria-label": "Advanced options" }, h("h2", null, "Options"));
    if (S.task === "add" || S.task === "organize") {
      opts.append(checkField("Priority decides first (-priority)", "priority", "A song from an earlier priority group always wins, before quality is compared."));
      opts.append(checkField("Sort “The …” by the next word (-ignore-the)", "ignoreThe", "“The Beatles” goes into B instead of T."));
    }
    if (S.task === "dedupe" || S.task === "add" || S.task === "organize" || S.task === "check") opts.append(checkField("Decode every media file completely (-deep)", "deep", "Finds damaged audio and video, but is slow."));
    opts.append(h("div", { class: "field" }, h("div", { class: "label" }, "Output folder (-o)"),
      folderInput(S.f.out, (v) => setField("out", v), "Output folder"),
      h("p", { class: "hint" }, "Empty: organize-output next to the library.")));
    fields.append(opts);
  }
  add(frag, fields);
  add(frag, h("p", { class: "resolved", id: "resolved", "aria-live": "polite" }));
  add(frag, h("div", { class: "actions" }, S.task === "dedupe"
    ? h("button", { class: "btn primary", id: "start", type: "button", disabled: true, onclick: () => {
      S.view = "options";
      S.notice = null;
      render();
      window.scrollTo(0, 0);
    } }, "Next")
    : h("button", { class: "btn primary", id: "start", type: "button", disabled: true, onclick: S.task === "restore" ? loadAside : S.task === "reports" ? () => openReports(organizeRequest()) : startAnalysis },
      S.task === "check" ? "Check songs" : S.task === "restore" ? "Look" : S.task === "reports" ? "Show reports" : "Check first"),
  h("span", { class: "muted small" }, S.task === "dedupe" ? "Next you choose what to tidy up."
    : S.task === "check" ? "Nothing in the library is changed. It can take a while for a big library."
    : S.task === "restore" ? "You choose what to put back on the next page."
    : S.task === "reports" ? "Nothing is changed."
    : "Nothing is changed yet. You see what would happen and decide then."),
  ));
  if (advanced()) {
    add(frag, h("h2", null, "Earlier runs"), h("div", { id: "history" }, historyList()));
  }
  queueMicrotask(updateResolved);
  return frag;
}

function historyList() {
  if (!S.history) return h("p", { class: "muted small" }, "Pick a library to see its earlier runs.");
  const runs = S.history.runs.filter((r) => r.moves > 0);
  if (!runs.length) return h("p", { class: "muted small" }, "No runs yet with this output folder.");
  return h("ul", { class: "history" }, runs.map((r) => h("li", null,
    h("span", null, r.time ? new Date(r.time).toLocaleString() : r.id),
    h("span", { class: "num" }, plural(r.moves, "move", "moves")),
    r.undone ? h("span", { class: "muted" }, "undone")
      : r.next ? h("button", { class: "link", type: "button", onclick: () => undo(S.resolved.library, S.resolved.outDir) }, "Undo this run")
      : h("span", { class: "muted" }, "kept"),
  )));
}

async function startJob(path, body) {
  S.notice = null;
  try {
    const job = await post(path, body);
    rememberLibrary(body?.library);
    S.job = job;
    S.waitingFor = job.id;
    S.view = "running";
    render();
    if (S.lastJob?.id === job.id && S.lastJob.state !== "running") onJobEnd(S.lastJob);
  } catch (e) {
    S.notice = { kind: "bad", title: e.message, details: e.details };
    render();
  }
}

// loadAside lists what is set aside for the restore task and shows the browser.
async function loadAside() {
  const btn = document.getElementById("start");
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Looking…";
  }
  try {
    S.aside = await post("/api/setaside/list", organizeRequest());
    rememberLibrary(S.aside.config.library);
    S.pick = new Set();
    S.open = new Set();
    S.view = "aside";
    S.notice = null;
  } catch (e) {
    S.notice = { kind: "bad", title: e.message, details: e.details };
  }
  render();
  window.scrollTo(0, 0);
}

// openReports lists the reports of the output folder of cfg ({ library, out } or a result's
// { library, outDir }) and shows the viewer.
async function openReports(cfg) {
  try {
    const res = await post("/api/reports/list", { library: cfg.library, out: cfg.out ?? cfg.outDir });
    S.reports = { ...res, shown: null };
    rememberLibrary(res.config.library);
    S.view = "reports";
    S.notice = null;
  } catch (e) {
    S.notice = { kind: "bad", title: e.message, details: e.details };
  }
  render();
  window.scrollTo(0, 0);
}

async function showReport(name) {
  const r = S.reports;
  try {
    r.shown = await post("/api/reports/read", { library: r.config.library, out: r.config.outDir, name });
  } catch (e) {
    r.shown = { name, text: e.message, truncated: false };
  }
  render();
}

function viewReports() {
  const r = S.reports;
  const frag = h("div");
  add(frag, h("h1", null, r.files.length ? "Reports" : "No reports yet"));
  add(frag, noticeBox(S.notice));
  add(frag, h("p", { class: "muted" }, "In ", h("span", { class: "path" }, r.config.outDir), "."));
  if (r.files.length) {
    add(frag, h("ul", { class: "history" }, r.files.map((f) => h("li", null,
      h("button", { class: "link", type: "button", "aria-pressed": String(r.shown?.name === f.name), onclick: () => showReport(f.name) }, f.name),
      h("span", { class: "muted small" }, new Date(f.modified).toLocaleString()),
      h("span", { class: "num" }, `${fmt(Math.max(1, Math.round(f.size / 1024)))} KB`)))));
  }
  if (r.shown) {
    add(frag, h("h2", null, r.shown.name), h("pre", { class: "report-text", tabindex: "0" }, r.shown.text),
      r.shown.truncated ? h("p", { class: "muted small" }, "Only the first part is shown. Open the file in the output folder to read all of it.") : null);
  }
  add(frag, h("div", { class: "actions" }, h("button", { class: "btn", type: "button", onclick: () => {
    S.view = "setup";
    S.notice = null;
    render();
    validate();
  } }, "Back")));
  return frag;
}

function startAnalysis() {
  startJob(`/api/${S.task === "remove" ? "remove" : S.task === "check" ? "check" : "organize"}/analyse`, organizeRequest(true));
}

// patternsField is the text box of the remove task: one artist or "Artist - Title" per line.
function patternsField() {
  const area = h("textarea", { id: "patterns", rows: "8", spellcheck: "false", "aria-describedby": "patterns-hint",
    placeholder: "ABBA\nQueen - Bohemian Rhapsody",
    oninput: (e) => {
      S.f.patterns = e.target.value;
      saveForm();
      updateResolved();
      validateSoon();
    } });
  area.value = S.f.patterns;
  const load = h("input", { type: "file", accept: ".txt,text/plain", hidden: true, onchange: async (e) => {
    const f = e.target.files[0];
    if (!f) return;
    const text = (await f.text()).replace(/\r\n?/g, "\n").trimEnd();
    setField("patterns", S.f.patterns.trim() ? `${S.f.patterns.trimEnd()}\n${text}` : text);
    area.value = S.f.patterns;
    e.target.value = "";
    updateResolved();
    validateSoon();
  } });
  return h("div", { class: "field" },
    h("label", { class: "label", for: "patterns" }, "Artists and songs to remove"),
    area,
    h("p", { class: "hint", id: "patterns-hint" },
      "One per line: an artist (“ABBA” also finds “ABBA & Friends”) or a song written “Artist - Title” (the duet and solo version). ",
      "Capitals, accents and punctuation do not matter. Lines starting with # are ignored. You see what matches before anything moves."),
    h("button", { class: "btn", type: "button", onclick: () => load.click() }, "Load from a file…"),
    load);
}

// ---------------------------------------------------------------- tidy-up options

function viewOptions() {
  const frag = h("div");
  add(frag, h("h1", null, "What should be tidied up?"));
  add(frag, h("p", { class: "muted" }, "In ", h("span", { class: "path" }, S.f.library.trim()), "."));
  add(frag, noticeBox(S.notice), systemNotice() || "");
  const t = S.f.tidy;
  const start = h("button", { class: "btn primary", type: "button", disabled: !anyTidy(tidyRequest()), onclick: startAnalysis }, "Check first");
  const set = (key, on) => {
    t[key] = on;
    saveForm();
    start.disabled = !anyTidy(tidyRequest());
  };
  for (const g of TIDY_GROUPS) {
    const off = g.media && !mediaOK();
    const box = h("fieldset", { class: "group" }, h("legend", null, g.title));
    if (off) box.append(h("p", { class: "hint small muted" }, "Needs ffmpeg, see above."));
    for (const o of g.options) {
      box.append(h("label", { class: `check${off ? " off" : ""}` },
        h("input", { type: "checkbox", checked: !off && t[o.key], disabled: off, onchange: (e) => set(o.key, e.target.checked) }),
        h("span", null, o.label, h("span", { class: "hint small muted" }, h("br"), o.hint)),
      ));
    }
    frag.append(box);
  }
  add(frag, h("p", { class: "muted small" }, "Set-aside songs and files are moved to the output folder, never deleted. ", TIDY_UNDO));
  add(frag, h("div", { class: "actions" },
    start,
    h("button", { class: "btn", type: "button", onclick: () => {
      S.view = "setup";
      S.notice = null;
      render();
      validate();
    } }, "Back"),
    h("span", { class: "muted small" }, "Nothing is changed yet. Audio and video are worked on only when you go ahead."),
  ));
  return frag;
}

function undo(library, out) {
  startJob("/api/organize/undo", { library, out });
}

// ---------------------------------------------------------------- running

// A fixed melody for the note bars: lane (0 = top) and length per note.
const MELODY = [[2, 3], [2, 2], [1, 3], [0, 4], [1, 2], [2, 3], [3, 5], [2, 2], [1, 2], [1, 3], [0, 2], [0, 4],
  [1, 3], [2, 2], [3, 3], [3, 2], [2, 4], [1, 2], [2, 3], [3, 6]];

function viewRunning() {
  const j = S.job || {};
  const notes = h("div", { class: "notes", "aria-hidden": "true" });
  let x = 0;
  const total = MELODY.reduce((s, [, len]) => s + len + 1, 0);
  for (const [lane, len] of MELODY) {
    notes.append(h("i", { "data-at": String(x / total), style: {
      left: `${(x / total) * 100}%`, width: `${(len / total) * 100}%`, top: `${lane * 16 + 4}px`,
    } }));
    x += len + 1;
  }
  const tidy = j.kind === "tidy-apply";
  const box = h("div", null,
    h("h1", null, tidy ? "Tidying up" : JOB_TITLES[(j.kind || "").replace(/^tidy-/, "organize-")] || "Working"),
    h("div", { class: "stage" },
      notes,
      h("div", { class: "lyric", id: "lyric", role: "progressbar", "aria-valuemin": "0", "aria-valuemax": "100" },
        h("span", { class: "base" }), h("span", { class: "sung", "aria-hidden": "true" })),
      h("div", { class: "counter", id: "counter" }),
    ),
    h("div", { class: "actions" },
      h("button", { class: "btn", type: "button", id: "stop", onclick: stopJob }, "Stop"),
      h("span", { class: "muted small", id: "stop-hint" },
        tidy ? "Stopping finishes the files being worked on. Run the tidy-up again later to do the rest."
          : STOP_HINTS[j.kind] || "Stopping changes nothing."),
    ),
  );
  queueMicrotask(updateRunning);
  return box;
}

function updateRunning() {
  const j = S.job;
  const lyric = document.getElementById("lyric");
  if (!j || !lyric) return;
  const pct = j.total > 0 ? Math.min(100, (j.done / j.total) * 100) : 0;
  const text = (j.dryRun && DRY_PHASES[j.phase]) || PHASES[j.phase] || "Getting started";
  lyric.querySelector(".base").textContent = text;
  const sung = lyric.querySelector(".sung");
  sung.textContent = text;
  lyric.style.setProperty("--p", `${pct}%`);
  lyric.setAttribute("aria-valuenow", String(Math.round(pct)));
  lyric.setAttribute("aria-valuetext", `${text}: ${fmt(j.done)} of ${fmt(j.total)}`);
  for (const n of document.querySelectorAll(".notes i")) n.classList.toggle("sung", Number(n.dataset.at) * 100 < pct);
  const parts = [];
  if (j.total > 0) parts.push(`${fmt(j.done)} of ${fmt(j.total)}`);
  if (!j.dryRun && j.stats?.moved) parts.push(`${plural(j.stats.moved, "folder", "folders")} moved`);
  if (!j.dryRun && j.stats?.written) parts.push(`${plural(j.stats.written, "file", "files")} written`);
  if (!j.dryRun && j.stats?.replaced) parts.push(`${plural(j.stats.replaced, "file", "files")} converted`);
  if (j.stats?.failed) parts.push(`${plural(j.stats.failed, "problem", "problems")}`);
  document.getElementById("counter").textContent = parts.join(", ");
}

async function stopJob() {
  const btn = document.getElementById("stop");
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Stopping…";
  }
  try {
    await post("/api/job/cancel");
  } catch (e) {
    S.notice = { kind: "bad", title: e.message };
  }
}

// ---------------------------------------------------------------- job events

function connectEvents() {
  const es = new EventSource("/api/job/events");
  es.addEventListener("job", (e) => {
    const j = JSON.parse(e.data);
    S.lastJob = j;
    if (j.id !== S.logJob) {
      S.logJob = j.id;
      S.logs = [];
      renderLogs();
    }
    if (j.id !== S.waitingFor) return;
    S.job = j;
    if (j.state === "running") updateRunning();
    else onJobEnd(j);
  });
  es.addEventListener("log", (e) => {
    const lines = JSON.parse(e.data);
    S.logs.push(...lines);
    if (S.logs.length > 5000) S.logs.splice(0, S.logs.length - 5000);
    appendLogs(lines);
  });
}

// resultFolders returns the library and output folder a job's result names, or null.
function resultFolders(j) {
  return j.result?.library ? { library: j.result.library, outDir: j.result.outDir } : null;
}

async function onJobEnd(j) {
  if (S.handled.has(j.id)) return;
  S.handled.add(j.id);
  S.waitingFor = null;
  const failed = (title) => ({ kind: "bad", title, text: j.error, details: j.details });
  const tidy = isTidyJob(j);
  try {
    switch (j.kind.replace(/^tidy-/, "organize-")) {
      case "organize-analyse":
        S.rem = S.chk = null;
        if (j.state === "done") {
          S.org = await get(`/api/organize/analysis?id=${encodeURIComponent(j.result.analysis)}`);
          S.open = new Set();
          S.view = "review";
        } else {
          S.view = tidy ? "options" : "setup";
          S.notice = j.state === "cancelled" ? { kind: "", title: "Stopped. Nothing was changed." } : failed("The check did not finish");
        }
        break;
      case "organize-apply":
        S.rem = S.chk = null;
        if (j.state === "done" || j.state === "cancelled") {
          // From the result: after a reload S.org is not loaded.
          if (!S.org && j.result?.analysis) S.org = await get(`/api/organize/analysis?id=${encodeURIComponent(j.result.analysis)}`).catch(() => null);
          S.done = { kind: "organize", job: j, stopped: j.state === "cancelled", cfg: resultFolders(j) || S.org?.config };
          S.view = "done";
        } else if (j.result?.planChanged) {
          S.org = await get(`/api/organize/analysis?id=${encodeURIComponent(j.result.analysis)}`);
          S.view = "review";
          S.notice = tidy
            ? { kind: "bad", title: "Something changed since you checked, so no song folders were moved", text: "The song texts and leftover files were taken care of already; audio and video were not worked on yet. Here is the new song plan. Look through it again before you go ahead.", details: advanced() ? j.details : null }
            : { kind: "bad", title: "Something changed since you checked, so nothing was moved", text: "Here is the new plan. Look through it again before you move anything.", details: advanced() ? j.details : null };
        } else if (tidy && resultFolders(j)) {
          S.done = { kind: "organize", job: j, stopped: false, cfg: resultFolders(j) };
          S.view = "done";
          S.notice = failed("Not everything could be done");
        } else if (j.stats?.moved && resultFolders(j)) {
          // Some songs moved before it failed: offer the undo.
          S.done = { kind: "organize", job: j, stopped: true, cfg: resultFolders(j) };
          S.view = "done";
          S.notice = failed("Moving did not finish");
        } else {
          // After a reload S.org is not loaded: the result names the analysis.
          if (!S.org && j.result?.analysis) S.org = await get(`/api/organize/analysis?id=${encodeURIComponent(j.result.analysis)}`);
          S.view = S.org ? "review" : "setup";
          S.notice = failed("Moving did not finish");
        }
        break;
      case "setaside-restore":
        S.done = { kind: "organize", restore: true, job: j, stopped: j.state === "cancelled", cfg: resultFolders(j) || S.aside?.config };
        S.view = "done";
        if (j.state === "failed") S.notice = failed("Not everything could be put back");
        break;
      case "check-analyse":
        if (j.state === "done") {
          S.chk = await get(`/api/check/analysis?id=${encodeURIComponent(j.result.analysis)}`);
          S.rem = S.org = null;
          S.open = new Set();
          S.view = "review";
        } else {
          S.view = "setup";
          S.notice = j.state === "cancelled" ? { kind: "", title: "Stopped." } : failed("The check did not finish");
        }
        break;
      case "check-apply":
        if (j.state === "done" || j.state === "cancelled" || (j.stats?.moved && resultFolders(j))) {
          S.done = { kind: "organize", check: true, job: j, stopped: j.state !== "done", cfg: resultFolders(j) || S.chk?.config };
          S.view = "done";
          if (j.state === "failed") S.notice = failed("Not everything could be done");
        } else {
          S.view = S.chk ? "review" : "setup";
          S.notice = failed("The check did not finish");
        }
        break;
      case "remove-analyse":
        if (j.state === "done") {
          S.rem = await get(`/api/remove/analysis?id=${encodeURIComponent(j.result.analysis)}`);
          S.org = S.chk = null;
          S.open = new Set();
          S.view = "review";
        } else {
          S.view = "setup";
          S.notice = j.state === "cancelled" ? { kind: "", title: "Stopped. Nothing was changed." } : failed("The check did not finish");
        }
        break;
      case "remove-apply":
        if (j.state === "done" || j.state === "cancelled") {
          S.done = { kind: "organize", remove: true, job: j, stopped: j.state === "cancelled", cfg: resultFolders(j) || S.rem?.config };
          S.view = "done";
        } else if (j.result?.planChanged) {
          S.rem = await get(`/api/remove/analysis?id=${encodeURIComponent(j.result.analysis)}`);
          S.view = "review";
          S.notice = { kind: "bad", title: "Something changed since you checked, so nothing was moved", text: "Here is the new plan. Look through it again before you remove anything.", details: advanced() ? j.details : null };
        } else if (j.stats?.moved && resultFolders(j)) {
          S.done = { kind: "organize", remove: true, job: j, stopped: true, cfg: resultFolders(j) };
          S.view = "done";
          S.notice = failed("Removing did not finish");
        } else {
          S.view = S.rem ? "review" : "setup";
          S.notice = failed("Removing did not finish");
        }
        break;
      case "organize-undo":
        // Never the folders of an earlier result: a retry must undo the same journal.
        S.done = { kind: "undo", job: j, cfg: resultFolders(j) };
        S.view = "done";
        if (j.state === "failed") S.notice = failed("Undo did not finish");
        break;
      default:
        S.view = "setup";
    }
    render();
  } catch (e) {
    S.view = "setup";
    S.notice = { kind: "bad", title: e.message };
    render();
  }
  window.scrollTo(0, 0);
}

// ---------------------------------------------------------------- log panel

const LEVELS = { trace: 0, debug: 1, info: 2, warning: 3, error: 4, fatal: 5, panic: 6 };
const logPre = document.getElementById("log-lines");
const logLevel = document.getElementById("log-level");

function logLine(l) {
  const t = new Date(l.time).toLocaleTimeString();
  return h("span", { class: `lv-${l.level}` }, `${t} ${l.level.padEnd(7)} ${l.msg}\n`);
}

function appendLogs(lines) {
  const min = LEVELS[logLevel.value];
  for (const l of lines) if (LEVELS[l.level] >= min) logPre.append(logLine(l));
  while (logPre.childNodes.length > 5000) logPre.firstChild.remove();
  document.getElementById("log-count").textContent = S.logs.length ? plural(S.logs.length, "line", "lines") : "";
  if (document.getElementById("log-follow").checked) logPre.scrollTop = logPre.scrollHeight;
}

function renderLogs() {
  logPre.replaceChildren();
  appendLogs(S.logs);
}
logLevel.addEventListener("change", renderLogs);

// ---------------------------------------------------------------- organize review

const songName = (s) => (s.artist || s.title ? `${s.artist || "?"} – ${s.title || "?"}` : s.rel);
const where = (s) => h("span", { class: "path" }, `${s.label}: ${s.rel}`);

function qualityText(q) {
  const parts = [];
  if (q.video) parts.push(q.videoHeight ? `video ${q.videoHeight}p` : "video");
  else parts.push("no video");
  if (q.audioKbps) parts.push(`${q.audioKbps} kbit/s audio`);
  parts.push(q.cover ? "cover" : "no cover");
  if (q.background) parts.push("background");
  if (q.warnings) parts.push(plural(q.warnings, "warning", "warnings"));
  return parts.join(", ");
}

async function setExcluded(refs, on) {
  const ex = new Set(S.org.excluded);
  for (const r of refs) on ? ex.add(r) : ex.delete(r);
  try {
    S.org = await post("/api/organize/exclude", { analysis: S.org.id, exclude: [...ex] });
    S.notice = null;
  } catch (e) {
    S.notice = { kind: "bad", title: e.message };
  }
  const y = window.scrollY;
  render();
  window.scrollTo(0, y);
}

// setJoined sets the pairs of songs treated as one song (the whole list) and plans again.
async function setJoined(pairs) {
  try {
    S.org = await post("/api/organize/join", { analysis: S.org.id, join: pairs });
    S.notice = null;
  } catch (e) {
    S.notice = { kind: "bad", title: e.message };
  }
  const y = window.scrollY;
  render();
  window.scrollTo(0, y);
}

function cappedList(items, renderItem, key) {
  const limit = S.open.has(`${key}:all`) ? Infinity : 200;
  const ul = h("ul", { class: "songs" }, items.slice(0, limit).map(renderItem));
  if (items.length > limit) {
    return h("div", null, ul, h("button", { class: "link", type: "button", onclick: () => {
      S.open.add(`${key}:all`);
      render();
    } }, `Show all ${fmt(items.length)}`));
  }
  return ul;
}

function tallyItem(key, kind, n, sentence, body) {
  const open = S.open.has(key) && n > 0;
  const id = `tally-${key}`;
  return h("li", { class: `kind-${kind}` },
    h("button", { class: "tally-head", type: "button", "aria-expanded": String(open), "aria-controls": id, disabled: n === 0, onclick: () => {
      open ? S.open.delete(key) : S.open.add(key);
      render();
    } },
      h("span", { class: "n" }, fmt(n)),
      h("span", null, sentence),
      n > 0 ? h("span", { class: "chev", "aria-hidden": "true" }, open ? "Hide" : "Show") : null,
    ),
    open ? h("div", { class: "tally-body", id }, body()) : null,
  );
}

function viewOrganizeReview() {
  const v = S.org;
  const songs = v.songs;
  const by = (action) => songs.map((s, i) => ({ ...s, i })).filter((s) => s.action === action);
  const dupsOf = (i) => songs.filter((s) => s.action === "duplicate" && s.other === i);
  const sum = v.summary;
  const moving = sum.toLibrary + sum.duplicates + sum.invalid;
  const t = v.tidy;
  const fileChanges = t ? t.textFiles.length + t.leftovers.length + t.brokenFolders.length : 0;
  const media = t ? [t.options.audio && "the volume is evened out", t.options.video && "the videos are converted"].filter(Boolean) : [];
  const frag = h("div");
  add(frag, h("h1", null, "This is what would happen"));
  add(frag, noticeBox(S.notice));
  if (v.applied) add(frag, noticeBox({ kind: "good", title: "This plan was carried out. Check again for a new one." }));
  if (t?.done) add(frag, noticeBox({ kind: "", text: "The song texts and leftover files were taken care of already, when you went ahead the last time." }));
  for (const note of v.notes) add(frag, noticeBox({ kind: "", text: note }));
  if (t) {
    const pathItem = (r) => h("li", null, h("span", { class: "path" }, r));
    const files = h("ul", { class: "tally" });
    if (t.options.utf8 || t.options.fileTags || t.options.fillTags) {
      files.append(tallyItem("text", "library", t.textFiles.length, t.textFiles.length === 1 ? "song text is fixed" : "song texts are fixed",
        () => cappedList(t.textFiles, pathItem, "text")));
    }
    if (t.options.leftovers) {
      files.append(tallyItem("leftovers", "dup", t.leftovers.length, t.leftovers.length === 1 ? "leftover file is set aside" : "leftover files are set aside",
        () => cappedList(t.leftovers, pathItem, "leftovers")));
    }
    if (t.options.broken) {
      files.append(tallyItem("emptyfolders", "bad", t.brokenFolders.length, t.brokenFolders.length === 1 ? "folder or file without a playable song is set aside" : "folders and files without a playable song are set aside",
        () => cappedList(t.brokenFolders, pathItem, "emptyfolders")));
    }
    if (files.childNodes.length) add(frag, h("h2", null, "Files"), files);
    if (t.songs) add(frag, h("h2", null, "Songs"));
  }

  const keeperItem = (s) => {
    const dups = dupsOf(s.i);
    return h("li", null,
      h("div", { class: "song-line" },
        h("div", null,
          h("div", { class: "song-name" }, songName(s), s.duet ? h("span", { class: "badge" }, "duet") : null),
          h("div", { class: "song-meta" }, s.action === "library" ? ["to ", h("span", { class: "path" }, s.target), " from ", where(s)] : where(s)),
          dups.length ? h("ul", { class: "versions" }, dups.map((d) => h("li", null, "Also found in ", where(d), ": set aside (", d.reason, ")"))) : null,
        ),
        h("button", { class: "link", type: "button", onclick: () => setExcluded([s.ref, ...dups.map((d) => d.ref)], true) }, t ? "Move none of its versions" : "Leave this song alone"),
      ),
    );
  };
  const dupItem = (s) => h("li", null,
    h("div", { class: "song-line" },
      h("div", null,
        h("div", { class: "song-name" }, songName(s), s.markedCopy ? h("span", { class: "badge dup" }, "copy") : null),
        h("div", { class: "song-meta" }, where(s)),
        h("div", { class: "song-meta" }, "Kept instead: ", s.other >= 0 ? where(songs[s.other]) : "?", ". Why: ", s.reason, "."),
        advanced() && s.other >= 0 ? h("div", { class: "song-meta" }, "This one: ", qualityText(s.quality), ". Kept one: ", qualityText(songs[s.other].quality), ".") : null,
      ),
      h("button", { class: "link", type: "button", onclick: () => setExcluded([s.ref], true) }, "Leave it where it is"),
    ),
  );
  const invalidItem = (s) => h("li", null,
    h("div", { class: "song-line" },
      h("div", null,
        h("div", { class: "song-name" }, songName(s)),
        h("div", { class: "song-meta" }, where(s)),
        h("div", { class: "song-meta" }, "Problems: ", (s.errors || []).join(", ") || s.reason),
        s.other >= 0 ? h("div", { class: "song-meta" }, "A working version is kept: ", where(songs[s.other])) : null,
      ),
      h("button", { class: "link", type: "button", onclick: () => setExcluded([s.ref], true) }, "Leave it where it is"),
    ),
  );
  const leftItem = (s) => h("li", null,
    h("div", { class: "song-name" }, songName(s)),
    h("div", { class: "song-meta" }, where(s)),
    h("div", { class: "song-meta" }, s.reason),
  );
  const excludedItem = (s) => h("li", null,
    h("div", { class: "song-line" },
      h("div", null, h("div", { class: "song-name" }, songName(s)), h("div", { class: "song-meta" }, where(s))),
      h("button", { class: "link", type: "button", onclick: () => setExcluded([s.ref], false) }, "Include it again"),
    ),
  );
  const left = by("left").filter((s) => !s.excluded);
  const excluded = by("left").filter((s) => s.excluded);
  const pairs = v.similar;

  const tally = h("ul", { class: "tally" });
  const songsShown = !t || t.songs;
  if (songsShown && (sum.toLibrary || v.mode !== "dedupe")) {
    tally.append(tallyItem("library", "library", sum.toLibrary, sum.toLibrary === 1 ? "song moves into the library" : "songs move into the library",
      () => cappedList(by("library"), keeperItem, "library")));
  }
  if (songsShown) tally.append(tallyItem("inplace", "stay", sum.inPlace,
    v.mode === "dedupe" ? (sum.inPlace === 1 ? "song stays in the library as it is" : "songs stay in the library as they are")
      : (sum.inPlace === 1 ? "song is already where it belongs" : "songs are already where they belong"),
    () => cappedList(by("in-place"), keeperItem, "inplace")));
  if (songsShown && (!t || t.options.dedupe)) tally.append(tallyItem("dup", "dup", sum.duplicates, sum.duplicates === 1 ? "duplicate is set aside" : "duplicates are set aside",
    () => cappedList(by("duplicate"), dupItem, "dup")));
  if (songsShown && (!t || t.options.broken)) tally.append(tallyItem("bad", "bad", sum.invalid, sum.invalid === 1 ? "broken song is set aside" : "broken songs are set aside",
    () => cappedList(by("invalid"), invalidItem, "bad")));
  if (songsShown) tally.append(tallyItem("left", "stay", left.length, left.length === 1 ? "song stays where it is, for a reason" : "songs stay where they are, each for a reason",
    () => cappedList(left, leftItem, "left")));
  if (excluded.length) {
    tally.append(tallyItem("excluded", "excluded", excluded.length, excluded.length === 1 ? "song you chose to leave alone" : "songs you chose to leave alone",
      () => cappedList(excluded, excludedItem, "excluded")));
  }
  if (songsShown && (!t || t.options.dedupe)) tally.append(tallyItem("similar", "stay", pairs.length, pairs.length === 1 ? "pair of songs looks alike but is kept apart" : "pairs of songs look alike but are kept apart",
    () => h("div", null,
      h("p", { class: "muted small" }, "Both songs of each pair are kept. If they are the same song, say so: the better version is kept and the other one is set aside."),
      cappedList(pairs, (p) => h("li", null,
        h("div", { class: "song-name" }, songName(songs[p.a]), " and ", songName(songs[p.b])),
        h("div", { class: "song-meta" }, where(songs[p.a]), " / ", where(songs[p.b])),
        h("div", { class: "song-meta" }, p.evidence),
        h("button", { class: "link", type: "button", onclick: () => setJoined([...v.joined, [songs[p.a].ref, songs[p.b].ref]]) }, "Same song, keep only the better version"),
      ), "similar"))));
  if (v.joined.length) {
    tally.append(tallyItem("joined", "move", v.joined.length, v.joined.length === 1 ? "pair you chose to treat as the same song" : "pairs you chose to treat as the same song",
      () => h("ul", { class: "songs" }, v.joined.map((pair, k) => {
        const [a, b] = pair.map((ref) => songs.find((x) => x.ref === ref));
        return h("li", null,
          h("div", { class: "song-name" }, a ? songName(a) : pair[0], " and ", b ? songName(b) : pair[1]),
          h("button", { class: "link", type: "button", onclick: () => setJoined(v.joined.filter((_, n) => n !== k)) }, "Keep them apart again"));
      }))));
  }
  if (tally.childNodes.length) add(frag, tally);
  if (media.length) {
    add(frag, h("h2", null, "Audio and video"),
      h("p", null, `When you go ahead, ${media.join(" and ")} after the steps above. This is checked file by file then: files that were worked on before are skipped.`));
  }

  const out = v.config.outDir;
  add(frag, h("p", { class: "muted" }, t
    ? ["Set-aside songs and files go to ", h("span", { class: "path" }, out), ". ", TIDY_UNDO, t.options.audio ? " The original audio files are kept in the output folder." : ""]
    : ["Set-aside songs go to ", h("span", { class: "path" }, out), " (duplicates and invalid). Every move is recorded, so you can undo the whole run afterwards."]));
  const todo = moving + fileChanges + media.length;
  const moveLabel = t ? (todo ? "Go ahead" : "Nothing to do")
    : moving ? `Move ${plural(moving, "song folder", "song folders")}` : "Nothing to move";
  add(frag, h("div", { class: "actions" },
    h("button", { class: "btn primary", type: "button", disabled: !todo || v.applied, onclick: async () => {
      const aside = [sum.duplicates ? plural(sum.duplicates, "duplicate", "duplicates") : "", sum.invalid ? plural(sum.invalid, "broken song", "broken songs") : ""].filter(Boolean);
      const ok = await confirmDialog(t ? "Tidy up now?" : `${moveLabel}?`, [
        t && t.textFiles.length ? `${plural(t.textFiles.length, "song text is", "song texts are")} fixed.` : "",
        t && t.leftovers.length + t.brokenFolders.length ? `${plural(t.leftovers.length + t.brokenFolders.length, "file or folder goes", "files and folders go")} to ${out}.` : "",
        aside.length ? `${aside.join(" and ")} ${sum.duplicates + sum.invalid === 1 ? "goes" : "go"} to ${out}.` : "",
        sum.toLibrary ? `${plural(sum.toLibrary, "song moves", "songs move")} into ${v.config.library}.` : "",
        media.length ? `Then ${media.join(" and ")}${t.options.video ? ". Converted videos cannot be undone" : ""}.` : "",
        moving && t && fileChanges ? "The song texts and leftover files are handled first. If any song changed since the check, no song folder is moved after that and you see the new plan." : "",
        moving && !(t && fileChanges) ? "If any song changed since the check, no song folder is moved and you see the new plan first." : "",
      ].filter(Boolean), moveLabel);
      if (ok) startJob("/api/organize/apply", { analysis: v.id });
    } }, moveLabel),
    h("button", { class: "btn", type: "button", onclick: startAnalysis }, "Check again"),
    t ? h("button", { class: "btn", type: "button", onclick: () => {
      S.view = "options";
      S.notice = null;
      render();
    } }, "Change what to do") : null,
    h("button", { class: "btn", type: "button", onclick: () => {
      S.view = "setup";
      S.notice = null;
      render();
      validate();
    } }, "Change folders"),
  ));
  if (advanced() && songsShown) add(frag, organizeTable(v));
  return frag;
}

const ASIDE_SECTIONS = {
  invalid: ["Broken songs and folders", "Songs and folders that could not be played or were empty."],
  duplicates: ["Duplicates", "The versions that lost against a better copy of the same song."],
  removed: ["Removed songs", "Songs taken out of the library on your request."],
  "extra-files": ["Leftover files", "Files no song used, such as old covers or a second song text."],
};

function viewAside() {
  const v = S.aside;
  const frag = h("div");
  const items = v.items;
  const key = (it) => `${it.section}/${it.rel}`;
  add(frag, h("h1", null, items.length ? "What was set aside" : "Nothing is set aside"));
  add(frag, noticeBox(S.notice));
  add(frag, h("p", { class: "muted" }, "In ", h("span", { class: "path" }, v.config.outDir), ". Nothing is deleted: choose what goes back to the library."));
  const update = () => {
    const n = S.pick.size;
    go.disabled = n === 0;
    go.textContent = n ? `Put back ${plural(n, "item", "items")}` : "Choose what to put back";
    for (const c of document.querySelectorAll("input[data-key]")) c.checked = S.pick.has(c.dataset.key);
  };
  const go = h("button", { class: "btn primary", type: "button", disabled: true, onclick: async () => {
    const chosen = items.filter((it) => S.pick.has(key(it)));
    const ok = await confirmDialog(`Put back ${plural(chosen.length, "item", "items")}?`, [
      "They go back to where they came from. Nothing is overwritten: an item whose old place is taken stays here.",
      "Every move is recorded, so you can undo it afterwards.",
    ], "Put back");
    if (ok) startJob("/api/setaside/restore", { library: v.config.library, out: v.config.outDir, items: chosen.map((it) => ({ section: it.section, rel: it.rel })) });
  } }, "Choose what to put back");
  const row = (it) => {
    const note = it.blocked ? it.blocked : !it.exact && advanced() ? `probably goes back to ${it.origin}` : "";
    return h("li", null, h("label", { class: it.blocked ? "check off" : "check" },
      h("input", { type: "checkbox", "data-key": key(it), disabled: !!it.blocked, checked: S.pick.has(key(it)), onchange: (e) => {
        e.target.checked ? S.pick.add(key(it)) : S.pick.delete(key(it));
        update();
      } }),
      h("span", null, it.name ? h("strong", null, it.name, " ") : null, h("span", { class: "path" }, it.rel),
        note ? h("span", { class: "hint small muted" }, h("br"), note) : null)));
  };
  const tally = h("ul", { class: "tally" });
  for (const section of Object.keys(ASIDE_SECTIONS)) {
    const list = items.filter((it) => it.section === section);
    if (!list.length) continue;
    const [title, hint] = ASIDE_SECTIONS[section];
    tally.append(tallyItem(`as-${section}`, "stay", list.length, `${title}: ${hint}`, () => {
      const free = list.filter((it) => !it.blocked);
      const all = free.length > 0 && free.every((it) => S.pick.has(key(it)));
      const limit = S.open.has(`as-${section}:all`) ? Infinity : 200;
      return h("div", null,
        h("label", { class: "check" }, h("input", { type: "checkbox", checked: all, disabled: !free.length, onchange: (e) => {
          for (const it of free) e.target.checked ? S.pick.add(key(it)) : S.pick.delete(key(it));
          update();
        } }), h("span", null, "Choose all")),
        h("ul", { class: "songs" }, list.slice(0, limit).map(row)),
        list.length > limit ? h("button", { class: "link", type: "button", onclick: () => {
          S.open.add(`as-${section}:all`);
          render();
        } }, `Show all ${fmt(list.length)}`) : null);
    }));
  }
  if (tally.childNodes.length) add(frag, tally);
  add(frag, h("div", { class: "actions" }, go,
    h("button", { class: "btn", type: "button", onclick: loadAside }, "Look again"),
    h("button", { class: "btn", type: "button", onclick: () => {
      S.view = "setup";
      S.notice = null;
      render();
      validate();
    } }, "Change folders")));
  queueMicrotask(update);
  return frag;
}

function viewCheckReview() {
  const v = S.chk;
  const rep = v.report;
  const frag = h("div");
  const broken = rep.songs.filter((x) => x.status === "broken");
  const warned = rep.songs.filter((x) => x.status === "suspicious");
  const ok = rep.checked - broken.length - warned.length;
  add(frag, h("h1", null, broken.length || warned.length ? "Some songs have problems" : "No problems found"));
  add(frag, noticeBox(S.notice));
  add(frag, h("p", null, `${plural(rep.checked, "song", "songs")} checked in `, h("span", { class: "path" }, v.config.library), `: ${fmt(ok)} fine, ${fmt(broken.length)} broken, ${fmt(warned.length)} with warnings.`));
  for (const n of rep.notes || []) add(frag, noticeBox({ kind: "", title: n }));
  const song = (x) => h("li", null, h("strong", null, x.path),
    x.notMoved ? h("span", { class: "muted small" }, ` (stays: ${x.notMoved})`) : null,
    h("ul", { class: "findings" }, x.findings.map((f) => h("li", { class: `sev-${f.severity}` },
      f.message, advanced() ? h("span", { class: "muted small" }, ` [${f.code}${f.line ? `, line ${f.line}` : ""}]`) : null))));
  const tally = h("ul", { class: "tally" });
  tally.append(tallyItem("ck-broken", "move", broken.length, broken.length === 1 ? "song is broken (it has errors)" : "songs are broken (they have errors)",
    () => cappedList(broken, song, "ck-broken")));
  tally.append(tallyItem("ck-warned", "stay", warned.length, warned.length === 1 ? "song has warnings only" : "songs have warnings only",
    () => cappedList(warned, song, "ck-warned")));
  add(frag, tally);
  if (advanced()) {
    const counts = new Map();
    for (const x of rep.songs) for (const f of x.findings) counts.set(f.code, (counts.get(f.code) || 0) + 1);
    if (counts.size) {
      add(frag, h("h2", null, "Problems by kind"), h("ul", { class: "history" }, [...counts].sort((a, b) => b[1] - a[1]).map(([code, n]) =>
        h("li", null, h("span", null, code), h("span", { class: "num" }, fmt(n))))));
    }
  }
  const out = v.config.outDir;
  add(frag, h("p", { class: "muted" }, "The full report is in ", h("span", { class: "path" }, `${out}/check_report.txt`), " and check_report.json."));
  const movable = broken.filter((x) => !x.notMoved && !x.moved);
  add(frag, h("div", { class: "actions" },
    advanced() && movable.length ? h("button", { class: "btn primary", type: "button", disabled: v.applied, onclick: async () => {
      const go = await confirmDialog(`Set aside ${plural(movable.length, "broken song", "broken songs")}?`, [
        `The check runs again, and every song with errors goes to ${out}/invalid. Nothing is deleted.`,
        "This is not part of undo: move a folder back by hand if you want it again.",
      ], "Set them aside");
      if (go) startJob("/api/check/apply", { analysis: v.id });
    } }, `Set aside ${plural(movable.length, "broken song", "broken songs")}…`) : null,
    h("button", { class: movable.length && advanced() ? "btn" : "btn primary", type: "button", onclick: startAnalysis }, "Check again"),
    h("button", { class: "btn", type: "button", onclick: () => {
      S.view = "setup";
      S.notice = null;
      render();
      validate();
    } }, "Change folders"),
  ));
  return frag;
}

function viewRemoveReview() {
  const v = S.rem;
  const rep = v.report;
  const frag = h("div");
  const n = rep.removed.length;
  add(frag, h("h1", null, n ? `${plural(n, "song", "songs")} would be removed` : "Nothing matches"));
  add(frag, noticeBox(S.notice));
  add(frag, h("p", { class: "muted" }, `Checked ${plural(rep.summary.songs, "song", "songs")} in `, h("span", { class: "path" }, v.config.library), "."));
  const tally = h("ul", { class: "tally" });
  const songItem = (e) => h("li", null, h("strong", null, e.name), " ", h("span", { class: "path" }, e.song),
    advanced() ? h("span", { class: "muted small" }, ` (${e.pattern})`) : null);
  tally.append(tallyItem("rm-removed", "move", n, n === 1 ? "song is moved out of the library" : "songs are moved out of the library",
    () => cappedList(rep.removed, songItem, "rm-removed")));
  const left = rep.leftInPlace;
  if (left.length) {
    tally.append(tallyItem("rm-left", "stay", left.length, left.length === 1 ? "song matches but stays" : "songs match but stay",
      () => cappedList(left, (e) => h("li", null, h("strong", null, e.name), " ", h("span", { class: "path" }, e.song), h("br"), h("span", { class: "muted small" }, e.reason)), "rm-left")));
  }
  const un = rep.unmatched;
  if (un.length) {
    tally.append(tallyItem("rm-unmatched", "stay", un.length, un.length === 1 ? "line matches no song" : "lines match no song",
      () => h("ul", { class: "songs" }, un.map((u) => h("li", null, u)))));
  }
  add(frag, tally);
  const out = v.config.outDir;
  add(frag, h("p", { class: "muted" }, "Removed songs go to ", h("span", { class: "path" }, `${out}/removed`), ". Every move is recorded, so you can undo the whole run afterwards."));
  add(frag, h("div", { class: "actions" },
    h("button", { class: "btn primary", type: "button", disabled: !n || v.applied, onclick: async () => {
      const ok = await confirmDialog(`Remove ${plural(n, "song", "songs")}?`, [
        `${plural(n, "song folder goes", "song folders go")} to ${out}/removed. Nothing is deleted.`,
        "If any song changed since the check, nothing is moved and you see the new plan first.",
      ], `Remove ${plural(n, "song", "songs")}`);
      if (ok) startJob("/api/remove/apply", { analysis: v.id });
    } }, n ? `Remove ${plural(n, "song", "songs")}` : "Nothing to remove"),
    h("button", { class: "btn", type: "button", onclick: startAnalysis }, "Check again"),
    h("button", { class: "btn", type: "button", onclick: () => {
      S.view = "setup";
      S.notice = null;
      render();
      validate();
    } }, "Change the list"),
  ));
  return frag;
}

function organizeTable(v) {
  const wrap = h("section", { "aria-label": "All song folders" }, h("h2", null, "All song folders"));
  const filter = S.tableFilter || (S.tableFilter = { action: "", q: "" });
  const tbody = h("tbody");
  const more = h("div");
  const fill = () => {
    const q = filter.q.toLowerCase();
    const rows = v.songs.map((s, i) => ({ ...s, i })).filter((s) =>
      (!filter.action || s.action === filter.action) &&
      (!q || `${s.artist} ${s.title} ${s.label}:${s.rel} ${s.target}`.toLowerCase().includes(q)));
    const limit = filter.all ? rows.length : 500;
    tbody.replaceChildren(...rows.slice(0, limit).map((s) => h("tr", { class: s.excluded ? "excluded" : "" },
      h("td", null, songName(s)),
      h("td", { class: "path" }, `${s.label}:${s.rel}`),
      h("td", { class: `act-${s.action}` }, s.excluded ? "left (excluded)" : s.action),
      h("td", null, s.target ? h("span", { class: "path" }, s.target) : s.other >= 0 ? ["instead: ", h("span", { class: "path" }, v.songs[s.other].rel)] : null,
        s.reason && !s.excluded ? h("div", { class: "muted" }, s.reason) : null),
      h("td", { class: "r" }, s.tier),
      h("td", null, s.quality.video ? (s.quality.videoHeight ? `${s.quality.videoHeight}p` : "yes") : ""),
      h("td", { class: "r" }, s.quality.videoKbps || ""),
      h("td", { class: "r" }, s.quality.audioKbps || ""),
      h("td", null, [s.quality.cover ? "C" : "", s.quality.background ? "B" : "", s.quality.version ? "V" : ""].join(" ")),
      h("td", { class: "r" }, s.quality.warnings || ""),
    )));
    more.replaceChildren(rows.length > limit ? h("button", { class: "link", type: "button", onclick: () => {
      filter.all = true;
      fill();
    } }, `Show all ${fmt(rows.length)} rows`) : h("span", { class: "muted small" }, plural(rows.length, "row", "rows")));
  };
  const actions = ["", "library", "in-place", "duplicate", "invalid", "left"];
  wrap.append(h("div", { class: "filters" },
    h("input", { type: "text", class: "plain", placeholder: "Search artist, title or folder", "aria-label": "Search", value: filter.q, oninput: (e) => {
      filter.q = e.target.value;
      fill();
    } }),
    h("select", { "aria-label": "Decision", onchange: (e) => {
      filter.action = e.target.value;
      fill();
    } }, actions.map((a) => h("option", { value: a, selected: filter.action === a }, a || "every decision"))),
  ));
  wrap.append(h("div", { class: "table-wrap" }, h("table", null,
    h("thead", null, h("tr", null, ["Song", "Folder", "Decision", "Target or reason"].map((t) => h("th", null, t)),
      h("th", { class: "r" }, "Tier"), h("th", null, "Video"), h("th", { class: "r" }, "Video kbit/s"), h("th", { class: "r" }, "Audio kbit/s"),
      h("th", { title: "Cover, Background, #VERSION", class: "nowrap" }, "C B V"), h("th", { class: "r" }, "Warnings"))),
    tbody)), more);
  fill();
  const raw = h("details", { class: "plain" }, h("summary", null, "Show the plan as JSON"));
  raw.addEventListener("toggle", () => {
    if (raw.open && raw.childNodes.length === 1) raw.append(h("pre", { class: "json" }, JSON.stringify(v, null, 2)));
  }, { once: false });
  wrap.append(raw);
  return wrap;
}

// ---------------------------------------------------------------- done

function viewDone() {
  const d = S.done;
  const j = d.job;
  const st = j.stats || {};
  const frag = h("div");
  const again = h("button", { class: "btn", type: "button", onclick: () => {
    S.view = "setup";
    S.notice = null;
    render();
    validate();
  } }, "Start over");
  const reports = d.cfg ? h("button", { class: "btn", type: "button", onclick: () => openReports(d.cfg) }, "Read the reports") : null;
  if (d.kind === "organize" && j.result?.tidy) {
    const steps = j.result.steps || [];
    const songsMoved = steps.find((s) => s.step === "songs")?.stats.moved || 0;
    add(frag, h("h1", null, d.stopped ? "Stopped part-way" : j.state === "done" ? "Done" : "Not everything could be done"));
    add(frag, noticeBox(S.notice));
    const lines = steps.flatMap((s) => (STEP_DONE[s.step]?.(s.stats) || []).filter(Boolean));
    if (lines.length) add(frag, h("ul", { class: "done-list" }, lines.map((l) => h("li", null, l))));
    else if (j.state === "done") add(frag, h("p", null, "Nothing needed to be changed."));
    const errors = steps.filter((s) => s.error);
    if (errors.length && !S.notice) add(frag, noticeBox({ kind: "bad", title: `${plural(errors.length, "step", "steps")} did not finish`, details: errors.map((s) => `${PHASES[s.step] || s.step}: ${s.error}`) }));
    if (st.failed) add(frag, noticeBox({ kind: "bad", title: `${plural(st.failed, "file or folder", "files or folders")} could not be worked on`, text: advanced() ? "The log below says why." : "Switch to Advanced to see the log." }));
    if (d.stopped) add(frag, h("p", null, "Run the tidy-up again to finish the rest: what is done already is skipped."));
    if (d.cfg) add(frag, h("p", null, "Set-aside songs and files are in ", h("span", { class: "path" }, d.cfg.outDir), ", together with the reports. Look through them before you delete anything."));
    add(frag, h("div", { class: "actions" },
      songsMoved && d.cfg ? h("button", { class: "btn", type: "button", onclick: () => undo(d.cfg.library, d.cfg.outDir) }, "Put back the set-aside songs") : null,
      reports, again));
  } else if (d.restore) {
    add(frag, h("h1", null, d.stopped ? "Stopped part-way" : j.state === "done" && !st.failed ? "Done" : "Not everything could be put back"));
    add(frag, noticeBox(S.notice));
    add(frag, h("p", null, `${plural(st.moved, "item was", "items were")} put back${d.stopped ? " before you stopped" : ""}.`));
    if (st.failed) add(frag, noticeBox({ kind: "bad", title: `${plural(st.failed, "item", "items")} stayed where ${st.failed === 1 ? "it was" : "they were"}`,
      text: `Something was in the way, or it is not known where ${st.failed === 1 ? "it" : "they"} came from.${advanced() ? " The log below says which." : " Switch to Advanced to see the log."}` }));
    add(frag, h("div", { class: "actions" },
      st.moved && d.cfg ? h("button", { class: "btn", type: "button", onclick: () => undo(d.cfg.library, d.cfg.outDir) }, "Undo this run") : null,
      h("button", { class: "btn", type: "button", onclick: () => {
        if (S.aside) {
          loadAside();
        } else {
          S.view = "setup";
          render();
          validate();
        }
      } }, "Back to the list"),
      reports, again));
  } else if (d.check) {
    add(frag, h("h1", null, d.stopped ? "Stopped part-way" : "Done"));
    add(frag, noticeBox(S.notice));
    add(frag, h("p", null, `${plural(st.moved, "broken song folder", "broken song folders")} moved to invalid.`));
    if (st.failed) add(frag, noticeBox({ kind: "bad", title: `${plural(st.failed, "move", "moves")} failed`, text: advanced() ? "The log below says why." : "Switch to Advanced to see the log." }));
    if (d.cfg) add(frag, h("p", null, "They are in ", h("span", { class: "path" }, `${d.cfg.outDir}/invalid`), ". This cannot be undone with one click: move a folder back by hand if you want it again."));
    add(frag, h("div", { class: "actions" }, reports, again));
  } else if (d.kind === "organize") {
    add(frag, h("h1", null, d.stopped ? "Stopped part-way" : "Done"));
    add(frag, noticeBox(S.notice));
    add(frag, h("p", null, d.stopped
      ? `${plural(st.moved, "song folder was", "song folders were")} moved before you stopped. Undo puts them back.`
      : d.remove ? `${plural(st.moved, "song folder", "song folders")} removed from the library.`
      : `${plural(st.moved, "song folder", "song folders")} moved.`));
    if (st.failed) add(frag, noticeBox({ kind: "bad", title: `${plural(st.failed, "move", "moves")} failed`, text: advanced() ? "The log below says why." : "Switch to Advanced to see the log." }));
    if (d.cfg) add(frag, h("p", null, d.remove ? "Removed songs are in " : "Set-aside songs are in ", h("span", { class: "path" }, d.remove ? `${d.cfg.outDir}/removed` : d.cfg.outDir), ". Look through them before you delete anything."));
    add(frag, h("div", { class: "actions" },
      st.moved && d.cfg ? h("button", { class: "btn", type: "button", onclick: () => undo(d.cfg.library, d.cfg.outDir) }, "Undo this run") : null,
      reports, again));
  } else if (d.kind === "undo") {
    const stopped = j.state === "cancelled";
    add(frag, h("h1", null, j.state === "done" ? "Undone" : stopped ? "Stopped part-way" : "Undo did not finish"));
    add(frag, noticeBox(S.notice));
    add(frag, h("p", null, `${plural(st.moved, "song folder is", "song folders are")} back where ${st.moved === 1 ? "it was" : "they were"}.`
      + (stopped ? " Undo again to move back the rest." : "")));
    if (st.failed) {
      add(frag, noticeBox({ kind: "bad", title: `${plural(st.failed, "folder", "folders")} could not be moved back`,
        text: "If something is in the way, fix it (the log says what) and undo again: folders that are back already are skipped. A folder that is gone is not looked for again." }));
    }
    add(frag, h("div", { class: "actions" },
      (st.failed || stopped) && d.cfg ? h("button", { class: "btn", type: "button", onclick: () => undo(d.cfg.library, d.cfg.outDir) }, stopped ? "Undo again" : "Try the undo again") : null,
      reports, again));
  }
  return frag;
}

// ---------------------------------------------------------------- start

for (const b of document.querySelectorAll(".switch button")) {
  b.addEventListener("click", () => {
    S.mode = b.dataset.mode;
    localStorage.setItem("lm-mode", S.mode);
    render();
    if (S.view === "setup") validate();
  });
}

async function start() {
  if (!(await loadSystem())) return;
  try {
    const j = await get("/api/job");
    if (j && j.state === "running") {
      S.job = j;
      S.waitingFor = j.id;
      S.view = "running";
    }
  } catch {
    // shown by the next call
  }
  connectEvents();
  render();
  if (S.view === "setup") validate();
}

start();
