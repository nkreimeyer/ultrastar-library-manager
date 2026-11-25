// Golden cases of the overview page's way into the set-aside list.
"use strict";

module.exports = [
  // "Look at what is set aside" lists the library the overview counted, whatever the form holds.
  { name: "look at what is set aside lists the counted library", async run(env) {
    env.set("S.f.library", "/form/lib");
    env.set("S.task", "overview");
    env.set("S.overview", { library: "/counted/lib", outDir: "/counted/out", songs: 3, bytes: 10, withVideo: 1, withImage: 2, buckets: [], setAside: { duplicates: 2 } });
    env.set("S.view", "overview");
    env.run("render()");
    const btn = env.button(env.app(), "Look at what is set aside");
    if (!btn) return { buttons: env.buttons(env.app()) };
    await env.click(btn);
    return { task: env.run("S.task"), stored: env.store.get("lm-task"), requests: env.requests.filter((r) => r.url === "/api/setaside/list") };
  } },
];
