// Login: show the PIN (or the Seer's password) field once a name is picked.
document.addEventListener("change", (e) => {
  const radio = e.target.closest('#login-form input[name="user_id"]');
  if (!radio) return;
  const seer = radio.dataset.seer === "true";
  const group = document.getElementById("secret-group");
  const input = document.getElementById("secret");
  document.getElementById("secret-label").textContent = seer ? "Seer password" : "PIN";
  input.inputMode = seer ? "text" : "numeric";
  input.value = "";
  group.hidden = false;
  input.focus();
});

// Copy buttons: <button data-copy="#input-id">. navigator.clipboard only exists on HTTPS (or
// localhost), so over plain http://<nas>:port fall back to selecting the text and execCommand.
async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return true;
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  ta.setSelectionRange(0, text.length);
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } finally {
    document.body.removeChild(ta);
  }
  return ok;
}

document.addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-copy]");
  if (!btn) return;
  const el = document.querySelector(btn.dataset.copy);
  const text = el.value ?? el.textContent;
  let ok = false;
  try {
    ok = await copyText(text);
  } catch {
    ok = false;
  }
  if (ok) {
    btn.innerHTML = '<i class="bi bi-check2"></i> Copied';
  } else {
    // Last resort: leave the text selected so the user can press Ctrl/Cmd+C.
    el.focus();
    el.select?.();
    btn.innerHTML = '<i class="bi bi-cursor-text"></i> Press Ctrl/Cmd+C';
  }
});

// Keep the scroll position when a form re-renders the same page (htmx swaps #main in place);
// go to the top when a link opens a different page.
let hiScroll = null;
document.addEventListener("htmx:beforeRequest", () => { hiScroll = { x: window.scrollX, y: window.scrollY, path: location.pathname }; });
document.addEventListener("htmx:afterSettle", (e) => {
  const url = e.detail.xhr && e.detail.xhr.responseURL ? new URL(e.detail.xhr.responseURL) : null;
  const wholePage = e.detail.target && e.detail.target.id === "main";
  if (!wholePage) {
    // A partial update (challenge result, oracle reading): leave scrolling to hx-swap.
  } else if (hiScroll && url && url.pathname === hiScroll.path) {
    window.scrollTo(hiScroll.x, hiScroll.y);
  } else if (url) {
    window.scrollTo(0, 0);
  }
  hiScroll = null;
  initTooltips(document.getElementById("main") || document);
});
// Close tooltips on elements about to be replaced, so none are left floating.
document.addEventListener("htmx:beforeSwap", () => {
  document.querySelectorAll('[data-bs-toggle="tooltip"]').forEach((el) => bootstrap.Tooltip.getInstance(el)?.dispose());
});

// Bootstrap tooltips for the "?" help icons (tap to show on phones).
function initTooltips(root) {
  if (!window.bootstrap) return;
  root.querySelectorAll('[data-bs-toggle="tooltip"]').forEach((el) => bootstrap.Tooltip.getOrCreateInstance(el, { trigger: "hover focus" }));
}
document.addEventListener("DOMContentLoaded", () => initTooltips(document));

// ---------------------------------------------------------------- live updates
// Campaign pages subscribe to the campaign's event stream. Someone else's change refreshes the
// page, unless you're in the middle of something (typing, a challenge or oracle result on
// screen), in which case a notice offers to refresh. Handouts from the Seer open in a dialog.
let liveSource = null, liveURL = null, refreshTimer = null;

function connectLive() {
  const main = document.getElementById("main");
  const url = main ? main.dataset.live || null : null;
  if (url === liveURL) return;
  if (liveSource) liveSource.close();
  liveSource = null;
  liveURL = url;
  if (!url) return;
  liveSource = new EventSource(url);
  liveSource.addEventListener("changed", (e) => {
    const d = JSON.parse(e.data || "{}");
    if (String(d.actor) === (document.getElementById("main").dataset.user || "")) return; // our own change
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(refreshOrNotify, 400);
  });
  liveSource.addEventListener("handout", (e) => showHandout(JSON.parse(e.data || "{}")));
}

function busy() {
  const a = document.activeElement;
  // Typing in the chat panel doesn't count: refreshing the page beside it leaves the panel alone.
  if (a && !a.closest("#chat-drawer") && a.matches("input:not([type=checkbox]):not([type=radio]), textarea, select")) return true;
  const why = document.getElementById("why-input");
  if (why && why.value) return true;
  return !!document.querySelector("#challenge-out .alert, #challenge-out .border, #oracle-out .card");
}

function refreshOrNotify() {
  if (busy()) {
    toast("Something changed.", "Refresh", () => refreshPage());
  } else {
    refreshPage();
  }
}

function refreshPage() {
  htmx.ajax("GET", location.pathname + location.search, { target: "#main", select: "#main", swap: "outerHTML show:none" });
}

function toast(text, action, onAction) {
  const box = document.getElementById("toasts");
  if (!box || !window.bootstrap) return;
  box.innerHTML = "";
  const el = document.createElement("div");
  el.className = "toast align-items-center text-bg-secondary border-0";
  el.setAttribute("role", "status");
  el.innerHTML = '<div class="d-flex"><div class="toast-body"></div><button type="button" class="btn btn-sm btn-light me-2 my-auto"></button><button type="button" class="btn-close btn-close-white me-2 m-auto" data-bs-dismiss="toast" aria-label="Close"></button></div>';
  el.querySelector(".toast-body").textContent = text;
  const btn = el.querySelector(".btn-light");
  btn.textContent = action;
  btn.addEventListener("click", () => { bootstrap.Toast.getOrCreateInstance(el).hide(); onAction(); });
  box.appendChild(el);
  bootstrap.Toast.getOrCreateInstance(el, { autohide: false }).show();
}

function showHandout(h) {
  const modal = document.getElementById("handout-modal");
  if (!modal || !window.bootstrap) return;
  modal.querySelector("#handout-title").textContent = h.title || h.card || "From the Seer";
  const body = modal.querySelector("#handout-body");
  body.innerHTML = "";
  if (h.card) {
    const c = document.createElement("p");
    c.className = "fs-5";
    c.innerHTML = '<i class="bi bi-suit-diamond"></i> ';
    c.append(h.card);
    if (h.meaning) { const m = document.createElement("span"); m.className = "d-block small text-body-secondary"; m.textContent = h.meaning; c.appendChild(m); }
    body.appendChild(c);
  }
  if (h.body) { const p = document.createElement("p"); p.className = "hi-prose"; p.textContent = h.body; body.appendChild(p); }
  if (h.image_url) { const img = document.createElement("img"); img.src = h.image_url; img.alt = h.title || "handout"; img.className = "img-fluid rounded"; body.appendChild(img); }
  bootstrap.Modal.getOrCreateInstance(modal).show();
}

document.addEventListener("DOMContentLoaded", connectLive);
document.addEventListener("htmx:afterSettle", connectLive);

// ---------------------------------------------------------------- chat side panel
// The chat lives in layout.html's #chat-drawer, outside #main, so it stays open while hx-boost
// swaps pages beside it. #main says which campaign's chat belongs to the current page
// (data-chat-base); the panel reloads when that changes. The open state is remembered per
// browser, but only reopened by itself on wide screens, where it docks instead of covering.
const chatOpenKey = "hi-chat-open";
let chatLoadedBase = null;

function chatDrawer() { return document.getElementById("chat-drawer"); }
function chatIsOpen() { const d = chatDrawer(); return !!d && !d.hidden; }
function chatBase() {
  const m = document.getElementById("main");
  return (m && m.dataset.chatBase) || "/chat";
}

function setChatOpen(open, opts = {}) {
  const d = chatDrawer();
  if (!d) return false;
  d.hidden = !open;
  document.body.classList.toggle("hi-chat-open", open);
  document.querySelectorAll("[data-chat-toggle]").forEach((b) => b.setAttribute("aria-expanded", String(open)));
  try { localStorage.setItem(chatOpenKey, open ? "1" : ""); } catch { /* private window etc. */ }
  if (open) loadChat(opts);
  return true;
}

function loadChat({ ask = "", focus = false } = {}) {
  const base = chatBase();
  const m = document.getElementById("main");
  const where = document.getElementById("chat-drawer-where");
  if (where) where.textContent = (m && m.dataset.chatLabel) || "";
  const full = document.getElementById("chat-drawer-full");
  if (full) full.href = base;
  if (chatLoadedBase === base) {
    if (ask) { const ta = chatTextarea(); if (ta) ta.value = ask; }
    scrollChat();
    if (focus || ask) chatTextarea()?.focus();
    return;
  }
  chatLoadedBase = base;
  const url = base + "/panel" + (ask ? "?ask=" + encodeURIComponent(ask) : "");
  htmx.ajax("GET", url, { target: "#chat-drawer-body", swap: "innerHTML" }).then(() => {
    scrollChat();
    if (focus || ask) chatTextarea()?.focus();
  });
}

function chatTextarea() { return document.querySelector('#chat-drawer textarea[name="message"]'); }
function scrollChat() {
  document.querySelectorAll(".hi-chat-messages").forEach((el) => { el.scrollTop = el.scrollHeight; });
}

// Capture phase, so a Chat link inside #main opens the panel instead of hx-boost following it.
document.addEventListener("click", (e) => {
  const toggle = e.target.closest("[data-chat-toggle]");
  if (toggle && chatDrawer()) {
    e.preventDefault();
    e.stopPropagation();
    setChatOpen(!chatIsOpen(), { focus: true });
    return;
  }
  if (e.target.closest("[data-chat-close]")) {
    setChatOpen(false);
    return;
  }
  // <button data-chat-ask="…">: open the panel with a question ready to send.
  const ask = e.target.closest("[data-chat-ask]");
  if (ask && chatDrawer()) {
    e.preventDefault();
    e.stopPropagation();
    setChatOpen(true, { ask: ask.dataset.chatAsk });
  }
}, true);

document.addEventListener("keydown", (e) => {
  // Enter sends; Shift+Enter is a new line.
  const ta = e.target.closest?.(".hi-chat-send textarea");
  if (ta && e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    if (ta.value.trim()) ta.form.requestSubmit();
    return;
  }
  if (e.key === "Escape" && chatIsOpen() && document.activeElement?.closest("#chat-drawer")) setChatOpen(false);
});

document.addEventListener("htmx:afterSettle", (e) => {
  const t = e.detail.target;
  if (t && (t.id === "chat-drawer-body" || t.classList?.contains("hi-chat") || t.closest?.("#chat-drawer"))) scrollChat();
  if (t && t.id === "main" && chatIsOpen() && chatBase() !== chatLoadedBase) loadChat();
});

// Applying a change the chat suggested may alter the sheet on screen beside it.
document.addEventListener("agentChanged", () => refreshOrNotify());

document.addEventListener("DOMContentLoaded", () => {
  if (!chatDrawer()) return;
  let wasOpen = false;
  try { wasOpen = localStorage.getItem(chatOpenKey) === "1"; } catch { /* ignore */ }
  if (wasOpen && window.matchMedia("(min-width: 992px)").matches) setChatOpen(true);
  scrollChat();
});

// ---------------------------------------------------------------- creation wizard
// <button data-fill='{"set.burden":"Reckless"}'>: a choice that fills in its form (the book's
// options for a card, or Claude's suggestions). Nothing is saved until the player saves the step.
document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-fill]");
  if (!b) return;
  const form = b.closest("form");
  if (!form) return;
  let fill = {};
  try { fill = JSON.parse(b.dataset.fill || "{}"); } catch { return; }
  for (const [name, value] of Object.entries(fill)) {
    const el = form.querySelector(`[name="${CSS.escape(name)}"]`);
    if (!el) continue;
    el.value = value;
    el.dispatchEvent(new Event("input", { bubbles: true }));
  }
  form.querySelectorAll(".hi-choice.active").forEach((x) => x.classList.remove("active"));
  b.classList.add("active");
  updateSkillTotal(form);
});

document.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || e.isComposing) return;
  // Enter in a card box looks the card up instead of saving the step.
  const card = e.target.closest?.("[data-card-input]");
  if (card) {
    e.preventDefault();
    card.dispatchEvent(new Event("change", { bubbles: true }));
    return;
  }
  // Enter in "describe what you're thinking" asks for ideas instead of saving the step.
  const hint = e.target.closest?.('.hi-suggest input[name="hint"]');
  if (hint) {
    e.preventDefault();
    hint.closest(".hi-suggest").querySelector("[data-suggest]")?.click();
  }
});

// The skills step's running total (the server checks the limits when it's saved).
function updateSkillTotal(form) {
  if (!form || form.id !== "wiz-skills") return;
  const need = Number(form.dataset.need || 0);
  let total = 0;
  form.querySelectorAll('select[name^="skill."]').forEach((s) => { total += Number(s.value || 0); });
  const box = document.getElementById("skill-total");
  if (!box) return;
  box.textContent = `${total} of ${need} points used.` + (total < need ? ` Add ${need - total} more.` : total > need ? ` Remove ${total - need}.` : " Ready to continue.");
  box.classList.toggle("alert-success", total === need);
  box.classList.toggle("alert-warning", total !== need);
}
document.addEventListener("change", (e) => {
  if (e.target.matches?.('#wiz-skills select[name^="skill."]')) updateSkillTotal(e.target.form);
});
document.addEventListener("htmx:afterSettle", () => updateSkillTotal(document.getElementById("wiz-skills")));
document.addEventListener("DOMContentLoaded", () => updateSkillTotal(document.getElementById("wiz-skills")));
