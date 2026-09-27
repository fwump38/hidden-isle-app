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
  if (a && a.matches("input:not([type=checkbox]):not([type=radio]), textarea, select")) return true;
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
