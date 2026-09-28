// Shared by every widget: the MCP Apps host bridge (JSON-RPC over postMessage), and small DOM
// helpers. Widgets never build HTML from strings, so nothing from the campaign is parsed as markup.
const hi = (() => {
  const PROTOCOL = "2026-01-26";
  let nextID = 0;
  const pending = new Map();
  const handlers = {};

  const post = (msg) => window.parent.postMessage(msg, "*");
  function request(method, params) {
    return new Promise((resolve, reject) => {
      const id = ++nextID;
      pending.set(id, { resolve, reject });
      post({ jsonrpc: "2.0", id, method, params: params || {} });
    });
  }
  const notify = (method, params) => post({ jsonrpc: "2.0", method, params: params || {} });

  window.addEventListener("message", (e) => {
    if (e.source !== window.parent) return;
    const m = e.data;
    if (!m || m.jsonrpc !== "2.0") return;
    if (m.method) {
      let result = {};
      try {
        const h = handlers[m.method];
        if (h) result = h(m.params || {}) || {};
      } catch (err) {
        console.error(m.method, err);
      }
      if (m.id !== undefined && m.id !== null) post({ jsonrpc: "2.0", id: m.id, result });
      return;
    }
    const p = pending.get(m.id);
    if (!p) return;
    pending.delete(m.id);
    if (m.error) p.reject(new Error(m.error.message || "request failed"));
    else p.resolve(m.result);
  });

  function applyContext(ctx) {
    if (!ctx) return;
    if (ctx.theme) document.documentElement.dataset.theme = ctx.theme;
    const vars = ctx.styles && ctx.styles.variables;
    if (vars) for (const [k, v] of Object.entries(vars)) if (v) document.documentElement.style.setProperty(k, v);
  }

  // text is a tool result's text content, joined.
  const text = (res) => (res.content || []).filter((c) => c.type === "text").map((c) => c.text).join(" ");
  function data(res) {
    if (res.structuredContent) return res.structuredContent;
    try { return JSON.parse(text(res)); } catch { return null; }
  }

  // call runs one of the server's tools through the host and returns its structured result.
  async function call(name, args) {
    const res = await request("tools/call", { name, arguments: args || {} });
    if (res.isError) throw new Error(text(res) || name + " failed");
    return data(res);
  }

  // start wires the widget up: render(data) runs with the tool's result, and again whenever the
  // widget calls refresh().
  let renderFn = null, lastArgs = {}, toolName = "";
  async function start(opts) {
    renderFn = opts.render;
    toolName = opts.tool;
    handlers["ui/notifications/tool-input"] = (p) => { lastArgs = p.arguments || {}; };
    handlers["ui/notifications/tool-result"] = (p) => {
      if (p.isError) return fail(text(p) || "The tool failed.");
      renderFn(data(p));
    };
    handlers["ui/notifications/tool-cancelled"] = () => fail("Cancelled.");
    handlers["ui/notifications/host-context-changed"] = applyContext;
    handlers["ui/resource-teardown"] = () => ({});
    try {
      const r = await request("ui/initialize", {
        protocolVersion: PROTOCOL,
        appInfo: { name: "hidden-isle-" + opts.name, version: "1" },
        appCapabilities: {},
      });
      applyContext(r && r.hostContext);
    } catch (err) {
      console.warn("ui/initialize", err);
    }
    notify("ui/notifications/initialized");
    const size = () => notify("ui/notifications/size-changed", {
      width: Math.ceil(document.documentElement.scrollWidth),
      height: Math.ceil(document.documentElement.scrollHeight),
    });
    new ResizeObserver(size).observe(document.body);
  }

  async function refresh() {
    try { renderFn(await call(toolName, lastArgs)); } catch (err) { fail(err.message); }
  }

  function fail(msg) {
    const el = document.getElementById("status");
    if (el) { el.className = "status err"; el.textContent = msg; }
  }
  function ok(msg) {
    const el = document.getElementById("status");
    if (el) { el.className = "status ok"; el.textContent = msg; }
  }

  // change runs a write tool with the "why" box's reason, then refreshes the widget and tells the
  // model what changed (its view of the campaign would be stale otherwise).
  async function change(name, args, summary) {
    const why = document.getElementById("why-input");
    const reason = why && why.value.trim();
    try {
      const out = await call(name, Object.assign({ reason: reason || "changed in the Claude widget" }, args));
      if (why) why.value = "";
      ok((out && out.note) || summary || "Saved.");
      context(summary + (reason ? " (" + reason + ")" : ""));
      await refresh();
      return out;
    } catch (err) {
      fail(err.message);
      return null;
    }
  }

  function context(line) {
    if (!line) return;
    request("ui/update-model-context", { content: [{ type: "text", text: "In the Hidden Isle widget, the Seer: " + line }] }).catch(() => {});
  }
  const message = (t) => request("ui/message", { role: "user", content: [{ type: "text", text: t }] });

  // h builds an element: h("div", {class: "x", onclick: fn}, "text", child, [more]).
  function h(tag, props, ...kids) {
    const el = tag === "svg" || tag === "path" ? document.createElementNS("http://www.w3.org/2000/svg", tag) : document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (v === undefined || v === null || v === false) continue;
      if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
      else if (k === "value" && "value" in el) el.value = v;
      else el.setAttribute(k, v === true ? "" : v);
    }
    const add = (k) => {
      if (k === undefined || k === null || k === false) return;
      if (Array.isArray(k)) k.forEach(add);
      else el.append(k instanceof Node ? k : String(k));
    };
    kids.forEach(add);
    return el;
  }

  // pips: a row of dots; set(n) gets the new value (tap the top filled dot to lower it).
  function pips(value, max, set, label) {
    const out = h("span", { class: "pips", role: "group", "aria-label": label });
    for (let i = 1; i <= max; i++) {
      const next = i === value ? i - 1 : i;
      out.append(h("button", { type: "button", class: "pip" + (i <= value ? " on" : ""), title: String(i),
        "aria-label": (label ? label + ": " : "") + "set to " + next, disabled: !set, onclick: () => set(next) }));
    }
    return out;
  }

  // clock: a circle cut into segments, the filled ones shaded (as on the web app).
  function clock(filled, segments, size) {
    const r = size / 2 - 2, c = size / 2;
    const svg = h("svg", { class: "clock", width: size, height: size, viewBox: `0 0 ${size} ${size}`, role: "img",
      "aria-label": `${filled} of ${segments} segments filled` });
    for (let i = 0; i < segments; i++) {
      const a0 = 2 * Math.PI * i / segments - Math.PI / 2, a1 = 2 * Math.PI * (i + 1) / segments - Math.PI / 2;
      const d = `M${c} ${c} L${(c + r * Math.cos(a0)).toFixed(2)} ${(c + r * Math.sin(a0)).toFixed(2)} ` +
        `A${r} ${r} 0 0 1 ${(c + r * Math.cos(a1)).toFixed(2)} ${(c + r * Math.sin(a1)).toFixed(2)} Z`;
      svg.append(h("path", { class: "seg" + (i < filled ? " filled" : ""), d }));
    }
    return svg;
  }

  // mount replaces the widget's content, keeping open <details> (by id) open.
  const mount = (...kids) => {
    const root = document.getElementById("app");
    const open = new Set([...root.querySelectorAll("details[open][id]")].map((d) => d.id));
    root.replaceChildren(...kids.flat().filter((k) => k !== null && k !== undefined && k !== false));
    for (const id of open) { const d = document.getElementById(id); if (d) d.open = true; }
  };

  return { start, call, change, refresh, message, context, h, pips, clock, mount, ok, fail };
})();
