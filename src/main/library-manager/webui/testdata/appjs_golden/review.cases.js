// Golden cases of viewOrganizeReview: the review page of an organize, add or dedupe check
// (with and without the tidy-up part), its tally sections, the item buttons and the
// primary button's confirm dialog.
"use strict";

// ---------------------------------------------------------------- builders

const Q = (over) => ({ warnings: 0, video: false, cover: false, background: false, version: false, ...over });

function song(i, over) {
  return {
    ref: `r${i}`, label: "src", rel: `Folder ${i}`, folder: `/src/Folder ${i}`,
    artist: `Artist ${i}`, title: `Title ${i}`, tier: 1, quality: Q(), action: "left", other: -1,
    ...over,
  };
}

// summarize counts the actions the way the Go side does (overridable per case).
function summarize(songs, similar) {
  const n = (a) => songs.filter((s) => s.action === a).length;
  return {
    songs: songs.length, toLibrary: n("library"), inPlace: n("in-place"), duplicates: n("duplicate"),
    invalid: n("invalid"), left: n("left"), excluded: songs.filter((s) => s.excluded).length, possible: 0,
    similar: similar.length,
  };
}

function analysis({ mode = "organize", songs = [], similar = [], joined = [], notes = [], applied = false, tidy, summary, id = "a1" } = {}) {
  const v = {
    id, mode,
    config: { library: "/music/Library", outDir: "/music/organize-output", sources: [{ label: "src", path: "/src", tier: 1 }] },
    notes,
    summary: { ...summarize(songs, similar), ...summary },
    songs,
    possibleDuplicates: [],
    similar,
    excluded: songs.filter((s) => s.excluded).map((s) => s.ref),
    joined,
    applied,
  };
  if (tidy) v.tidy = tidy;
  return v;
}

const ALL_TIDY = { utf8: true, fileTags: true, fillTags: true, dedupe: true, broken: true, folders: true, leftovers: true, audio: true, video: false };

function tidyPart({ options, textFiles = [], leftovers = [], brokenFolders = [], done = false, songs = true } = {}) {
  return { options: { ...ALL_TIDY, ...options }, steps: [], textFiles, leftovers, brokenFolders, done, songs };
}

// The realistic song list used by most cases: two keepers moving into the library (index 0
// has a duplicate pointing at it: other = 0), an in-place duet, duplicates with other = 0
// and -1 (a marked copy), broken songs with and without errors and replacement, songs left
// for a reason, an excluded one and one without tags (name from the folder).
function richSongs() {
  return [
    song(0, { label: "src", rel: "ABBA - Dancing Queen", artist: "ABBA", title: "Dancing Queen", action: "library", target: "A/ABBA - Dancing Queen",
      quality: Q({ video: true, videoHeight: 1080, videoKbps: 4000, audioKbps: 320, cover: true, background: true, version: true }) }),
    song(1, { label: "lib", rel: "Q/Queen - Bohemian Rhapsody", artist: "Queen", title: "Bohemian Rhapsody", action: "in-place", target: "Q/Queen - Bohemian Rhapsody", duet: true, inLibrary: true,
      quality: Q({ video: true, audioKbps: 192, cover: true, warnings: 2 }) }),
    song(2, { label: "old", rel: "ABBA - Dancing Queen_dupl1", artist: "ABBA", title: "Dancing Queen", action: "duplicate", other: 0, markedCopy: true,
      reason: "a marked copy (_dupl)", quality: Q({ video: true, videoHeight: 720, audioKbps: 128, warnings: 1 }) }),
    song(3, { label: "old", rel: "Toto - Africa", artist: "Toto", title: "Africa", action: "duplicate", other: -1, reason: "lower audio bitrate",
      quality: Q({ audioKbps: 96 }) }),
    song(4, { label: "old", rel: "Queen - Bohemian Rhapsody (broken)", artist: "Queen", title: "Bohemian Rhapsody", action: "invalid", other: 1,
      errors: ["no-audio", "bad-bpm"], reason: "broken" }),
    song(5, { label: "old", rel: "Nobody - Nothing", artist: "Nobody", title: "", action: "invalid", other: -1, errors: [], reason: "no notes" }),
    song(6, { label: "src", rel: "Mojibake - Song", artist: "Mojibake", title: "Song", action: "left", reason: "the txt has an encoding problem" }),
    song(7, { label: "src", rel: "Kept - Here", artist: "Kept", title: "Here", action: "left", excluded: true, reason: "you chose to leave it alone" }),
    song(8, { label: "src", rel: "no tags folder", artist: "", title: "", action: "library", target: "#/no tags folder" }),
    song(9, { label: "src", rel: "Left - Two", artist: "Left", title: "", action: "left", reason: "target taken by a non-song folder" }),
  ];
}

const RICH_SIMILAR = [
  { a: 0, b: 3, evidence: "lyrics 95 % alike, chart within 30 ms" },
  { a: 6, b: 9, evidence: "same chart, other artist spelling" },
];
const RICH_JOINED = [["r1", "r4"], ["r6", "gone-ref"]];

const rich = (over = {}) => analysis({ songs: richSongs(), similar: RICH_SIMILAR, joined: RICH_JOINED, ...over });

const ALL_KEYS = ["text", "leftovers", "emptyfolders", "library", "inplace", "dup", "bad", "left", "excluded", "similar", "joined"];

// ---------------------------------------------------------------- page helpers

function show(env, v, { open = [], advanced = false, notice = null, task, library } = {}) {
  env.run(`S.org = ${JSON.stringify(v)}`);
  env.run(`S.view = "review"`);
  env.run(`S.mode = ${JSON.stringify(advanced ? "advanced" : "easy")}`);
  env.run(`S.open = new Set(${JSON.stringify(open)})`);
  env.run(`S.notice = ${JSON.stringify(notice)}`);
  if (task) env.run(`S.task = ${JSON.stringify(task)}`);
  if (library !== undefined) env.run(`S.f.library = ${JSON.stringify(library)}`);
  env.run("render()");
}

// outline is a compact text form of a view: one line per heading, notice, tally row, list item
// and button, in document order (the full snapshots are in the view cases).
// liText is the text of a list item: its names and meta lines, without buttons and nested lists.
function liText(li) {
  const out = [];
  const walk = (n) => {
    for (const k of n.children) {
      if (typeof k === "string") out.push(k);
      else if (k.tagName === "button" || k.tagName === "ul") continue;
      else if (/song-name|song-meta|path/.test(k.className)) out.push(k.textContent);
      else walk(k);
    }
  };
  walk(li);
  return out;
}

function outline(root) {
  const lines = [];
  const walk = (n, depth) => {
    if (typeof n === "string") return;
    const t = n.tagName;
    const text = n.textContent.replace(/\s+/g, " ").trim();
    if (/^h[1-6]$/.test(t)) lines.push(`${t}: ${text}`);
    else if (t === "div" && /notice/.test(n.className)) {
      lines.push(`notice[${n.className}]: ${text}`);
      return;
    } else if (t === "button") lines.push(`${"  ".repeat(depth)}button${isDisabled(n) ? "(disabled)" : ""}${n.attrs["aria-expanded"] ? `[${n.attrs["aria-expanded"]}]` : ""}: ${text}`);
    else if (t === "li" && n.parent && /songs|versions/.test(n.parent.className)) {
      lines.push(`${"  ".repeat(depth)}- ${liText(n).join(" | ")}`);
    } else if (t === "p") {
      lines.push(`${"  ".repeat(depth)}p${n.className ? `.${n.className.replace(/ /g, ".")}` : ""}: ${text}`);
      return;
    } else if (t === "section") lines.push(`section: ${n.attrs["aria-label"]}`);
    for (const k of n.children) walk(k, t === "li" ? depth + 1 : depth);
    if (t === "section") lines.push(`table rows: ${n.querySelectorAll("tr").length}`);
  };
  walk(root, 0);
  return lines;
}

const isDisabled = (el) => el.getAttribute("disabled") !== null;

const since = (env, n) => env.requests.slice(n);

// primaryButton is the first button of the actions row.
const primary = (env) => env.app().querySelector("div.actions").children[0];

// openConfirm clicks the primary button and returns the dialog's content as text.
async function openConfirm(env) {
  const b = primary(env);
  const label = b.textContent;
  // h() sets disabled as an attribute; a browser fires no click on a disabled button.
  const disabled = isDisabled(b);
  if (disabled) return { label, disabled };
  await env.click(b);
  const dlg = env.byId("confirm");
  return {
    label, disabled, open: dlg.open,
    title: dlg.querySelector("h2")?.textContent ?? null,
    lines: dlg.querySelectorAll("p").map((p) => p.textContent),
    buttons: env.buttons(dlg),
  };
}

// li finds the list item (of an open tally) whose song name text starts with name.
const itemButton = (env, name, text) => {
  const li = env.app().all().find((e) => e.tagName === "li" && e.querySelector("div.song-name") && e.querySelector("div.song-name").textContent.startsWith(name) && env.button(e, text));
  if (!li) throw new Error(`no item ${name} with ${text}`);
  return env.button(li, text);
};

const tallyHeads = (env) => env.app().querySelectorAll("button.tally-head").map((b) => ({
  kind: b.parent.className, n: b.children[0].textContent, text: b.children[1].textContent,
  expanded: b.attrs["aria-expanded"], disabled: isDisabled(b),
}));

// ---------------------------------------------------------------- cases

const cases = [];
const view = (name, v, opts) => cases.push({ name, run(env) {
  show(env, v, opts);
  return { app: env.snap(env.app()) };
} });
const lines = (name, v, opts) => cases.push({ name, run(env) {
  show(env, v, opts);
  return { outline: outline(env.app()), heads: tallyHeads(env), wide: env.app().className };
} });

// Full snapshots.
view("organize easy, closed", rich());
view("organize easy, all open", rich(), { open: ALL_KEYS });
view("organize advanced, closed", rich(), { advanced: true });
view("organize advanced, all open", rich(), { open: ALL_KEYS, advanced: true });
view("add easy, all open", rich({ mode: "add" }), { open: ALL_KEYS });
view("tidy all options, all open", rich({ mode: "dedupe", tidy: tidyPart({
  options: { video: true },
  textFiles: ["A/ABBA - Dancing Queen/song.txt", "Q/Queen/song.txt"],
  leftovers: ["A/ABBA - Dancing Queen/old.jpg", "B/extra.txt"],
  brokenFolders: ["Empty Folder", "Z/Lone.mp3"],
}) }), { open: ALL_KEYS });
view("tidy songs false advanced, all open", rich({ mode: "dedupe", tidy: tidyPart({ songs: false, textFiles: ["a.txt"] }) }), { open: ALL_KEYS, advanced: true });
view("tidy advanced songs true, closed", rich({ mode: "dedupe", tidy: tidyPart({ textFiles: ["a.txt"] }) }), { advanced: true });
view("empty organize", analysis({ songs: [] }));
view("tidy audio off video off", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { audio: false } }) }));
view("tidy audio off video on", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { audio: false, video: true } }) }));

// Outlines: modes, guards, texts.
lines("organize outline closed", rich());
lines("add outline all open", rich({ mode: "add" }), { open: ALL_KEYS });
lines("dedupe without toLibrary hides the library row", (() => {
  const songs = richSongs().map((s) => (s.action === "library" ? { ...s, action: "in-place", inLibrary: true } : s));
  return analysis({ mode: "dedupe", songs, similar: RICH_SIMILAR, joined: RICH_JOINED });
})(), { open: ALL_KEYS });
lines("dedupe with toLibrary shows the library row", rich({ mode: "dedupe" }), { open: ["library", "inplace"] });
lines("dedupe summary toLibrary 0 but library songs listed", rich({ mode: "dedupe", summary: { toLibrary: 0 } }), { open: ALL_KEYS });
lines("organize summary toLibrary 0 keeps a disabled row", rich({ summary: { toLibrary: 0 } }), { open: ["library"] });
lines("dedupe keeper in place with duplicates", analysis({ mode: "dedupe", songs: [
  song(0, { action: "in-place", target: "A/Artist 0 - Title 0", label: "lib" }),
  song(1, { action: "duplicate", other: 0, reason: "no video", label: "lib", rel: "B/dup one" }),
  song(2, { action: "duplicate", other: 0, reason: "a marked copy", markedCopy: true, label: "lib", rel: "C/dup two" }),
] }), { open: ALL_KEYS });
lines("singular texts", analysis({ mode: "dedupe", songs: [
  song(0, { action: "library", target: "A/x" }),
  song(1, { action: "in-place", target: "B/y" }),
  song(2, { action: "duplicate", other: 0, reason: "r" }),
  song(3, { action: "invalid", errors: ["e"] }),
  song(4, { action: "left", reason: "why" }),
  song(5, { action: "left", excluded: true }),
], similar: [{ a: 0, b: 1, evidence: "ev" }], joined: [["r0", "r1"]] }));
lines("singular texts organize", analysis({ songs: [
  song(0, { action: "in-place", target: "B/y" }),
] }));
lines("plural texts dedupe in place", analysis({ mode: "dedupe", songs: [
  song(0, { action: "in-place", target: "B/y" }), song(1, { action: "in-place", target: "C/y" }),
] }));
lines("zero rows are disabled even when open", analysis({ songs: [] }), { open: ALL_KEYS });
lines("applied", rich({ applied: true }));
lines("notes and notice", rich({ notes: ["The library holds an older journal.", "ffprobe is missing: video heights are unknown."] }),
  { notice: { kind: "bad", title: "Something failed", text: "details here", details: ["a", "b"] } });
lines("only excluded songs", analysis({ songs: [song(0, { action: "left", excluded: true })] }), { open: ["excluded"] });
lines("joined pair with unknown refs", analysis({ songs: [song(0, { action: "in-place", target: "x" })], joined: [["r0", "zz"], ["yy", "r0"]] }), { open: ["joined"] });

// Tidy guards.
const TIDY_FILES = { textFiles: ["t1.txt", "t2.txt"], leftovers: ["l1"], brokenFolders: ["b1", "b2"] };
lines("tidy all closed", rich({ mode: "dedupe", tidy: tidyPart(TIDY_FILES) }));
lines("tidy songs false, all open", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, songs: false }) }), { open: ALL_KEYS });
lines("tidy songs false without excluded or joined", analysis({ mode: "dedupe", songs: [song(0, { action: "in-place", target: "x" })],
  tidy: tidyPart({ ...TIDY_FILES, songs: false }) }), { open: ALL_KEYS });
lines("tidy dedupe off", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { dedupe: false } }) }), { open: ALL_KEYS });
lines("tidy broken off", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { broken: false } }) }), { open: ALL_KEYS });
lines("tidy only fillTags of the text options", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { utf8: false, fileTags: false } }) }));
lines("tidy only fileTags of the text options", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { utf8: false, fillTags: false } }) }));
lines("tidy no text options", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { utf8: false, fileTags: false, fillTags: false } }) }));
lines("tidy no leftovers option", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { leftovers: false } }) }));
lines("tidy no folders option", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { folders: false } }) }));
lines("tidy no file options: Songs heading only", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES,
  options: { utf8: false, fileTags: false, fillTags: false, leftovers: false, folders: false } }) }));
lines("tidy no file options and songs false", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ songs: false,
  options: { utf8: false, fileTags: false, fillTags: false, leftovers: false, folders: false } }) }));
lines("tidy done with notes and applied", rich({ mode: "dedupe", applied: true, notes: ["a note"], tidy: tidyPart({ done: true }) }),
  { notice: { kind: "good", title: "Saved" } });
lines("tidy singular files", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ textFiles: ["a"], leftovers: ["b"], brokenFolders: ["c"] }) }), { open: ALL_KEYS });
lines("tidy empty file rows are disabled", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { audio: false } }) }), { open: ALL_KEYS });
lines("tidy audio and video", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { video: true } }) }));
lines("tidy video only", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { audio: false, video: true } }) }));
lines("tidy keeper button text", rich({ mode: "dedupe", tidy: tidyPart() }), { open: ["library", "inplace"] });

// The output note's children ("" text node without audio).
for (const [name, opts] of [["output note tidy with audio", {}], ["output note tidy without audio", { audio: false }]]) {
  cases.push({ name, run(env) {
    show(env, analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: opts }) }));
    return env.snap(env.app().querySelectorAll("p.muted").pop());
  } });
}
cases.push({ name: "output note organize", run(env) {
  show(env, rich());
  return env.snap(env.app().querySelectorAll("p.muted").pop());
} });

// Top-level children: the order of the parts.
for (const [name, v, opts] of [
  ["top-level order organize advanced", rich({ notes: ["n1"], applied: true }), { advanced: true, notice: { kind: "bad", title: "x" } }],
  ["top-level order tidy", rich({ mode: "dedupe", notes: ["n1"], tidy: tidyPart({ ...TIDY_FILES, done: true }) }), { advanced: true }],
  ["top-level order tidy songs false", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, songs: false }) }), { advanced: true }],
]) {
  cases.push({ name, run(env) {
    show(env, v, opts);
    return env.app().children[0].children.map((k) => `${k.tagName}${k.className ? "." + k.className.replace(/ /g, ".") : ""}: ${k.textContent.slice(0, 60)}`);
  } });
}

// Long lists: capped at 200 with a "Show all" button that sets <key>:all.
cases.push({ name: "capped list shows 200 and the show-all button", run: async (env) => {
  const leftovers = Array.from({ length: 203 }, (_, i) => `file ${i}`);
  show(env, analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ leftovers }) }), { open: ["leftovers"] });
  const body = env.byId("tally-leftovers");
  const before = { items: body.querySelectorAll("li").length, buttons: env.buttons(body), first: body.querySelector("li").textContent };
  await env.click(env.button(body, "Show all 203"));
  const after = env.byId("tally-leftovers");
  return { before, after: { items: after.querySelectorAll("li").length, buttons: env.buttons(after), last: after.querySelectorAll("li").pop().textContent }, ...env.state(["open"]) };
} });
cases.push({ name: "capped list of exactly 200 has no button", run(env) {
  const songs = Array.from({ length: 200 }, (_, i) => song(i, { action: "library", target: `A/${i}` }));
  show(env, analysis({ songs }), { open: ["library"] });
  const body = env.byId("tally-library");
  return { items: body.querySelectorAll("ul.songs > li").length, tag: body.children[0].tagName, buttons: env.buttons(body).filter((b) => !b.startsWith("Leave")).length };
} });
cases.push({ name: "capped list of 201 songs", run(env) {
  const songs = Array.from({ length: 201 }, (_, i) => song(i, { action: "left", reason: "x" }));
  show(env, analysis({ songs }), { open: ["left"] });
  const body = env.byId("tally-left");
  return { tag: body.children[0].tagName, items: body.querySelectorAll("li").length, buttons: env.buttons(body) };
} });

// Toggling a row.
cases.push({ name: "tally head toggles S.open", run: async (env) => {
  show(env, rich());
  const head = () => env.app().querySelectorAll("button.tally-head").find((b) => b.parent.className === "kind-dup");
  await env.click(head());
  const opened = { ...env.state(["open"]), body: !!env.byId("tally-dup"), expanded: head().attrs["aria-expanded"], chev: head().children[2].textContent };
  await env.click(head());
  return { opened, closed: { ...env.state(["open"]), body: !!env.byId("tally-dup"), expanded: head().attrs["aria-expanded"], chev: head().children[2].textContent } };
} });
cases.push({ name: "tally head of the inplace row has id and aria", run(env) {
  show(env, rich(), { open: ["inplace"] });
  const b = env.app().querySelectorAll("button.tally-head")[1];
  return { attrs: env.snap(b).a, bodyId: env.byId("tally-inplace")?.className };
} });

// The primary button and its confirm dialog.
const confirmCase = (name, v, { ok = true, expect } = {}) => cases.push({ name, run: async (env) => {
  show(env, v);
  const n = env.requests.length;
  const dlg = await openConfirm(env);
  if (dlg.disabled) return { dlg };
  await env.confirm(ok);
  return { dlg, requests: since(env, n), after: env.byId("confirm").open, ...env.state(["view", "waitingFor"]) };
} });
confirmCase("confirm organize rich, ok", rich());
confirmCase("confirm organize rich, cancel", rich(), { ok: false });
confirmCase("confirm one duplicate only: goes", analysis({ mode: "dedupe", songs: [song(0, { action: "in-place", target: "x" }), song(1, { action: "duplicate", other: 0, reason: "r" })] }));
confirmCase("confirm one broken only: goes", analysis({ mode: "dedupe", songs: [song(0, { action: "invalid", errors: ["e"] })] }));
confirmCase("confirm duplicate and broken: go", analysis({ mode: "dedupe", songs: [song(0, { action: "in-place", target: "x" }), song(1, { action: "duplicate", other: 0, reason: "r" }), song(2, { action: "invalid", errors: ["e"] })] }));
confirmCase("confirm two duplicates", analysis({ mode: "dedupe", songs: [song(0, { action: "in-place", target: "x" }), song(1, { action: "duplicate", other: 0, reason: "r" }), song(2, { action: "duplicate", other: 0, reason: "r" })] }));
confirmCase("confirm toLibrary only, one song", analysis({ songs: [song(0, { action: "library", target: "A/x" })] }));
confirmCase("confirm toLibrary only, two songs", analysis({ mode: "add", songs: [song(0, { action: "library", target: "A/x" }), song(1, { action: "library", target: "A/y" })] }));
confirmCase("confirm nothing to move is disabled", analysis({ songs: [song(0, { action: "in-place", target: "x" })] }));
confirmCase("confirm applied is disabled", rich({ applied: true }));
confirmCase("confirm tidy texts leftovers and video", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { video: true } }) }));
confirmCase("confirm tidy, cancel", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, options: { video: true } }) }), { ok: false });
confirmCase("confirm tidy moving without file changes", rich({ mode: "dedupe", tidy: tidyPart({ options: { audio: false } }) }));
confirmCase("confirm tidy file changes without moving", analysis({ mode: "dedupe", songs: [song(0, { action: "in-place", target: "x" })], tidy: tidyPart({ ...TIDY_FILES, options: { audio: false } }) }));
confirmCase("confirm tidy one leftover only", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ leftovers: ["x"], options: { audio: false } }) }));
confirmCase("confirm tidy one broken folder only", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ brokenFolders: ["x"], options: { audio: false } }) }));
confirmCase("confirm tidy one text file only", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ textFiles: ["x"], options: { audio: false } }) }));
confirmCase("confirm tidy text files with text options off", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ textFiles: ["x", "y"], options: { utf8: false, fileTags: false, fillTags: false, audio: false } }) }));
confirmCase("confirm tidy media only audio", analysis({ mode: "dedupe", songs: [], tidy: tidyPart() }));
confirmCase("confirm tidy media only video", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { audio: false, video: true } }) }));
confirmCase("confirm tidy nothing to do is disabled", analysis({ mode: "dedupe", songs: [], tidy: tidyPart({ options: { audio: false } }) }));
confirmCase("confirm tidy songs false with file changes", rich({ mode: "dedupe", tidy: tidyPart({ ...TIDY_FILES, songs: false, options: { audio: false } }) }));

cases.push({ name: "confirm ok starts the job and shows running", run: async (env) => {
  env.answers["/api/organize/apply"] = () => env.reply(202, { id: "j9", kind: "organize-apply", state: "running" });
  show(env, rich());
  const n = env.requests.length;
  await openConfirm(env);
  await env.confirm(true);
  return { requests: since(env, n), ...env.state(["view", "waitingFor", "notice"]), job: env.run("S.job.id") };
} });
cases.push({ name: "confirm ok with a failing apply shows the notice", run: async (env) => {
  env.answers["/api/organize/apply"] = () => env.reply(409, { error: "a job is running" });
  show(env, rich());
  await openConfirm(env);
  await env.confirm(true);
  return { ...env.state(["view", "notice"]), first: outline(env.app()).slice(0, 2) };
} });

// Check again and Back.
cases.push({ name: "check again organize", run: async (env) => {
  show(env, rich(), { task: "organize", library: "/music/Library" });
  const n = env.requests.length;
  await env.click(env.button(env.app(), "Check again"));
  return since(env, n);
} });
cases.push({ name: "check again tidy dedupe", run: async (env) => {
  show(env, rich({ mode: "dedupe", tidy: tidyPart() }), { task: "dedupe", library: "/music/Library" });
  const n = env.requests.length;
  await env.click(env.button(env.app(), "Check again"));
  return since(env, n);
} });
cases.push({ name: "back without tidy goes to setup and validates", run: async (env) => {
  show(env, rich(), { task: "organize", library: "/music/Library", notice: { kind: "bad", title: "x" } });
  const n = env.requests.length;
  await env.click(env.button(env.app(), "Back"));
  return { ...env.state(["view", "notice"]), requests: since(env, n).map((r) => r.url), h1: env.app().querySelector("h1")?.textContent };
} });
cases.push({ name: "back with tidy goes to options without validating", run: async (env) => {
  show(env, rich({ mode: "dedupe", tidy: tidyPart() }), { task: "dedupe", library: "/music/Library", notice: { kind: "bad", title: "x" } });
  const n = env.requests.length;
  await env.click(env.button(env.app(), "Back"));
  return { ...env.state(["view", "notice"]), requests: since(env, n).map((r) => r.url), h1: env.app().querySelector("h1")?.textContent };
} });

// Item buttons: exclude and join requests.
const replanned = () => rich({ id: "a1", notes: ["replanned"] });
const itemCase = (name, v, open, songName, text, { answer = true, status = 200 } = {}) => cases.push({ name, run: async (env) => {
  if (answer) env.answers["/api/organize/exclude"] = env.answers["/api/organize/join"] = () => env.reply(status, status < 300 ? replanned() : { error: "the analysis is being applied" });
  env.window.scrollY = 321;
  show(env, v, { open });
  const n = env.requests.length;
  await env.click(itemButton(env, songName, text));
  return { requests: since(env, n), scrolls: env.scrolls, ...env.state(["notice"]), notes: env.run("S.org.notes"), page: outline(env.app()).slice(0, 3) };
} });
itemCase("keeper leave alone excludes it and its duplicates", rich(), ["library"], "ABBA – Dancing Queen", "Leave this song alone");
itemCase("keeper without duplicates", rich(), ["library"], "no tags folder", "Leave this song alone");
itemCase("in-place keeper leave alone", rich(), ["inplace"], "Queen – Bohemian Rhapsody", "Leave this song alone");
itemCase("tidy keeper move none of its versions", rich({ mode: "dedupe", tidy: tidyPart() }), ["library"], "ABBA – Dancing Queen", "Move none of its versions");
itemCase("dedupe in-place keeper with two duplicates", analysis({ mode: "dedupe", songs: [
  song(0, { action: "in-place", target: "x" }), song(1, { action: "duplicate", other: 0, reason: "r" }),
  song(2, { action: "duplicate", other: 3, reason: "r" }), song(3, { action: "in-place", target: "y" }), song(4, { action: "duplicate", other: 0, reason: "r" }),
], joined: [] }), ["inplace"], "Artist 0", "Leave this song alone");
itemCase("duplicate leave it where it is", rich(), ["dup"], "Toto – Africa", "Leave it where it is");
itemCase("invalid leave it where it is", rich(), ["bad"], "Nobody – ?", "Leave it where it is");
itemCase("excluded include it again", rich(), ["excluded"], "Kept – Here", "Include it again");
itemCase("exclude failure shows a notice", rich(), ["dup"], "Toto – Africa", "Leave it where it is", { status: 409 });
cases.push({ name: "same song posts the joined list plus the pair", run: async (env) => {
  env.answers["/api/organize/join"] = () => env.reply(200, replanned());
  show(env, rich(), { open: ["similar"] });
  const n = env.requests.length;
  const bs = env.app().all().filter((e) => e.tagName === "button" && e.textContent === "Same song, keep only the better version");
  await env.click(bs[1]);
  return { count: bs.length, requests: since(env, n), scrolls: env.scrolls, notes: env.run("S.org.notes") };
} });
cases.push({ name: "keep them apart posts the list without that pair", run: async (env) => {
  env.answers["/api/organize/join"] = () => env.reply(200, replanned());
  show(env, rich(), { open: ["joined"] });
  const n = env.requests.length;
  const bs = env.app().all().filter((e) => e.tagName === "button" && e.textContent === "Keep them apart again");
  await env.click(bs[0]);
  return { count: bs.length, requests: since(env, n) };
} });
cases.push({ name: "keep them apart the second pair", run: async (env) => {
  show(env, rich(), { open: ["joined"] });
  const n = env.requests.length;
  const bs = env.app().all().filter((e) => e.tagName === "button" && e.textContent === "Keep them apart again");
  await env.click(bs[1]);
  return since(env, n);
} });
cases.push({ name: "join failure shows a notice", run: async (env) => {
  env.answers["/api/organize/join"] = () => env.reply(409, { error: "already applied" });
  show(env, rich(), { open: ["similar"] });
  await env.click(env.button(env.app(), "Same song, keep only the better version"));
  return { ...env.state(["notice"]), page: outline(env.app()).slice(0, 2) };
} });
// The join buttons use the v.joined of the render; the exclude buttons read S.org at click time.
cases.push({ name: "stale join buttons use the joined list of their render", run: async (env) => {
  const next = rich({ joined: [["r6", "r9"], ["r0", "r3"]] });
  env.answers["/api/organize/join"] = () => env.reply(200, next);
  show(env, rich({ joined: [["r1", "r4"]] }), { open: ["similar", "joined"] });
  const same = env.button(env.app(), "Same song, keep only the better version");
  const apart = env.button(env.app(), "Keep them apart again");
  const n = env.requests.length;
  await env.click(same);
  await env.click(same);
  await env.click(apart);
  return since(env, n).map((r) => r.body);
} });
cases.push({ name: "pending join clicked twice sends the same list", run: async (env) => {
  show(env, rich({ joined: [] }), { open: ["similar"] });
  const bs = env.app().all().filter((e) => e.tagName === "button" && e.textContent === "Same song, keep only the better version");
  const n = env.requests.length;
  await env.click(bs[0]);
  await env.click(bs[1]);
  return since(env, n).map((r) => r.body);
} });
cases.push({ name: "stale exclude buttons read the current excluded list", run: async (env) => {
  const next = rich();
  next.excluded = ["r7", "r6"];
  env.answers["/api/organize/exclude"] = () => env.reply(200, next);
  show(env, rich(), { open: ["dup", "excluded"] });
  const leave = itemButton(env, "Toto – Africa", "Leave it where it is");
  const include = itemButton(env, "Kept – Here", "Include it again");
  const n = env.requests.length;
  await env.click(leave);
  await env.click(leave);
  await env.click(include);
  return since(env, n).map((r) => r.body);
} });

// The duplicate item: s.other = 0 and -1, advanced quality line.
for (const adv of [false, true]) {
  cases.push({ name: `duplicate items ${adv ? "advanced" : "easy"}`, run(env) {
    show(env, rich(), { open: ["dup"], advanced: adv });
    return env.snap(env.byId("tally-dup"));
  } });
}
cases.push({ name: "invalid items", run(env) {
  show(env, rich(), { open: ["bad"] });
  return env.snap(env.byId("tally-bad"));
} });
cases.push({ name: "invalid item with no errors field falls back to the reason", run(env) {
  const songs = [song(0, { action: "in-place", target: "x" }), song(1, { action: "invalid", reason: "only a reason", other: 0 })];
  show(env, analysis({ songs }), { open: ["bad"] });
  return outline(env.byId("tally-bad"));
} });
cases.push({ name: "duplicate whose keeper is index 0 advanced", run(env) {
  const songs = [song(0, { action: "in-place", target: "x", quality: Q({ video: true, videoHeight: 480, cover: true }) }), song(1, { action: "duplicate", other: 0, reason: "worse video", quality: Q({ warnings: 1 }) })];
  show(env, analysis({ songs }), { open: ["dup", "inplace"], advanced: true });
  return { dup: outline(env.byId("tally-dup")), keeper: outline(env.byId("tally-inplace")) };
} });
cases.push({ name: "similar section snapshot", run(env) {
  show(env, rich(), { open: ["similar"] });
  return env.snap(env.byId("tally-similar"));
} });
cases.push({ name: "joined section snapshot", run(env) {
  show(env, rich(), { open: ["joined"] });
  return env.snap(env.byId("tally-joined"));
} });
cases.push({ name: "keeper and excluded sections snapshot", run(env) {
  show(env, rich(), { open: ["library", "inplace", "left", "excluded"] });
  return ["library", "inplace", "left", "excluded"].map((k) => env.snap(env.byId(`tally-${k}`)));
} });
cases.push({ name: "advanced wide class follows the view", run(env) {
  show(env, rich(), { advanced: true });
  const wide = env.app().className;
  env.run(`S.mode = "easy"; render()`);
  return { wide, easy: env.app().className, log: env.byId("log").hidden };
} });

// A successful exclude or join clears an earlier notice; a join keeps the scroll position.
cases.push({ name: "exclude success clears an earlier notice", run: async (env) => {
  env.answers["/api/organize/exclude"] = () => env.reply(200, replanned());
  show(env, rich(), { open: ["dup"], notice: { kind: "bad", title: "old" } });
  await env.click(itemButton(env, "Toto – Africa", "Leave it where it is"));
  return { ...env.state(["notice"]), notices: outline(env.app()).filter((l) => l.startsWith("notice")) };
} });
cases.push({ name: "join success clears an earlier notice and keeps the scroll", run: async (env) => {
  env.answers["/api/organize/join"] = () => env.reply(200, replanned());
  env.window.scrollY = 321;
  show(env, rich(), { open: ["similar"], notice: { kind: "bad", title: "old" } });
  await env.click(env.button(env.app(), "Same song, keep only the better version"));
  return { ...env.state(["notice"]), scrolls: env.scrolls, notices: outline(env.app()).filter((l) => l.startsWith("notice")) };
} });
cases.push({ name: "keep them apart keeps the scroll", run: async (env) => {
  env.answers["/api/organize/join"] = () => env.reply(200, replanned());
  env.window.scrollY = 321;
  show(env, rich(), { open: ["joined"] });
  await env.click(env.button(env.app(), "Keep them apart again"));
  return { scrolls: env.scrolls };
} });
// The primary button applies the analysis it was drawn for, not a newer S.org.
cases.push({ name: "stale primary button applies its own analysis", run: async (env) => {
  show(env, rich({ id: "a1" }));
  env.run(`S.org = ${JSON.stringify(rich({ id: "a2" }))}`);
  const n = env.requests.length;
  await openConfirm(env);
  await env.confirm(true);
  return since(env, n).map((r) => ({ url: r.url, body: r.body }));
} });
cases.push({ name: "capped list of 1203 formats the show-all count", run(env) {
  const songs = Array.from({ length: 1203 }, (_, i) => song(i, { action: "left", reason: "x" }));
  show(env, analysis({ songs }), { open: ["left"] });
  return env.buttons(env.byId("tally-left")).filter((b) => b.startsWith("Show all"));
} });

module.exports = cases;
