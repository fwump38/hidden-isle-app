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
  if (wholePage) scrollToTarget();
  initTooltips(document.getElementById("main") || document);
});
// Close tooltips and help popovers on elements about to be replaced, so none are left floating.
document.addEventListener("htmx:beforeSwap", () => {
  document.querySelectorAll('[data-bs-toggle="tooltip"]').forEach((el) => bootstrap.Tooltip.getInstance(el)?.dispose());
  document.querySelectorAll("[data-hi-help]").forEach((el) => bootstrap.Popover.getInstance(el)?.dispose());
});

// Bootstrap tooltips (for any plain data-bs-toggle="tooltip").
function initTooltips(root) {
  if (!window.bootstrap) return;
  root.querySelectorAll('[data-bs-toggle="tooltip"]').forEach((el) => bootstrap.Tooltip.getOrCreateInstance(el, { trigger: "hover focus click" }));
}
document.addEventListener("DOMContentLoaded", () => initTooltips(document));

// ---------------------------------------------------------------- page cites → rule browser
// Every page cite on screen ("p. 15", "pp. 72, 100-103", "Sheet p. 3", "Ref p. 8") links to
// that page in the rule browser. The prefixes come from the rules manifest (<body data-cites>).
// A MutationObserver catches anything added later: htmx swaps, handouts.
const citeSkip = "a, button, select, option, textarea, input, script, style, code, pre, [contenteditable], [data-no-cites], .hi-page";
let citeRe = null, citeBooks = {};

function citePattern() {
  if (citeRe !== null) return citeRe;
  try { citeBooks = JSON.parse(document.body.dataset.cites || "{}"); } catch { citeBooks = {}; }
  // "Sheet p." → "Sheet"; plain "p." has no word before it.
  const words = Object.keys(citeBooks).map((p) => p.replace(/\s*p\.\s*$/, "")).filter(Boolean)
    .map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  if (!Object.keys(citeBooks).length) { citeRe = false; return citeRe; }
  const pre = words.length ? `(?:\\b(${words.join("|")})\\s+)?` : "()";
  const range = "\\d+(?:\\s*[-–]\\s*\\d+)?";
  citeRe = new RegExp(`${pre}\\b(pp?)\\.\\s?(${range}(?:\\s*,\\s*${range})*)`, "g");
  return citeRe;
}

function bookFor(word) {
  for (const [prefix, key] of Object.entries(citeBooks)) {
    const w = prefix.replace(/\s*p\.\s*$/, "");
    if ((word || "") === w) return key;
  }
  return null;
}

function citeLink(book, text) {
  const a = document.createElement("a");
  a.className = "hi-cite";
  a.href = `/rules/page/${book}/${parseInt(text.match(/\d+/)[0], 10)}`;
  a.textContent = text;
  a.title = "Open in the rule browser";
  return a;
}

function linkifyText(node, re) {
  const text = node.nodeValue;
  re.lastIndex = 0;
  if (!re.test(text)) return;
  re.lastIndex = 0;
  const frag = document.createDocumentFragment();
  let last = 0, m;
  while ((m = re.exec(text))) {
    const book = bookFor(m[1]);
    if (!book) continue;
    frag.append(text.slice(last, m.index));
    // "pp. 72, 100-103": the first link carries the prefix, the rest are bare numbers. A single
    // "p." takes only its first number ("p. 23, 2 harm" isn't a list).
    const nums = m[2] === "pp" ? m[3].split(/(\s*,\s*)/) : [m[3].match(/^\d+(?:\s*[-–]\s*\d+)?/)[0]];
    const head = m[0].slice(0, m[0].length - m[3].length);
    frag.append(citeLink(book, head + nums[0]));
    for (let i = 1; i < nums.length; i++) frag.append(i % 2 ? nums[i] : citeLink(book, nums[i]));
    last = m.index + head.length + nums.join("").length;
    re.lastIndex = last;
  }
  if (last === 0) return;
  frag.append(text.slice(last));
  const links = [...frag.querySelectorAll("a")];
  node.replaceWith(frag);
  // Inside #main, let hx-boost follow them like any other link (it only sees links it processed).
  if (window.htmx) links.forEach((a) => { if (a.closest("[hx-boost]")) htmx.process(a); });
}

function linkifyCites(root) {
  const re = citePattern();
  if (!re || !root) return;
  if (root.nodeType === Node.TEXT_NODE) {
    if (root.parentElement && !root.parentElement.closest(citeSkip)) linkifyText(root, re);
    return;
  }
  if (root.nodeType !== Node.ELEMENT_NODE || root.closest(citeSkip)) return;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => (n.parentElement.closest(citeSkip) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT),
  });
  const nodes = [];
  while (walker.nextNode()) nodes.push(walker.currentNode);
  nodes.forEach((n) => linkifyText(n, re));
}

const citeRoots = "#main, #handout-modal";
document.addEventListener("DOMContentLoaded", () => {
  document.querySelectorAll(citeRoots).forEach(linkifyCites);
  const obs = new MutationObserver((muts) => {
    for (const mu of muts) {
      for (const n of mu.addedNodes) {
        const el = n.nodeType === Node.ELEMENT_NODE ? n : n.parentElement;
        if (el && el.closest(citeRoots) && !(n.nodeType === Node.ELEMENT_NODE && n.matches("a.hi-cite"))) linkifyCites(n);
      }
    }
    obs.takeRecords(); // our own changes
  });
  obs.observe(document.body, { childList: true, subtree: true });
  scrollToTarget();
});
// Again once images and fonts are in, in case they moved the target.
window.addEventListener("load", scrollToTarget);

// The rule browser marks the element to open at (a page from a cite, a search hit).
function scrollToTarget() {
  const toc = document.querySelector(".hi-rules-toc");
  if (toc && window.matchMedia("(min-width: 992px)").matches) toc.open = true;
  const box = document.querySelector("#main [data-scroll-to]");
  const el = box && document.getElementById(box.dataset.scrollTo);
  if (!el) return;
  el.scrollIntoView({ block: "start", behavior: "instant" });
  el.classList.remove("hi-target");
  void el.offsetWidth;
  el.classList.add("hi-target");
}

// ---------------------------------------------------------------- help popovers
// The "?" icons ({{help}} in templates): a popover on hover, focus or tap, with its page cites
// linked. It stays open while the pointer is over it, so the links can be clicked; a tap pins it
// until the next tap elsewhere.
let helpHide = null;

function helpPopover(el) {
  return bootstrap.Popover.getOrCreateInstance(el, {
    trigger: "manual", html: true, placement: "top", customClass: "hi-help-pop",
    content: () => { const div = document.createElement("div"); div.textContent = el.dataset.hiHelp; linkifyCites(div); return div; },
  });
}
function showHelp(el) {
  if (!window.bootstrap) return;
  clearTimeout(helpHide);
  document.querySelectorAll("[data-hi-help][aria-describedby]").forEach((o) => { if (o !== el) hideHelp(o); });
  const p = helpPopover(el);
  if (!el.getAttribute("aria-describedby")) p.show();
}
function hideHelp(el) {
  delete el.dataset.pinned;
  bootstrap.Popover.getInstance(el)?.hide();
}
function hideHelpSoon() {
  clearTimeout(helpHide);
  helpHide = setTimeout(() => {
    document.querySelectorAll("[data-hi-help][aria-describedby]").forEach((el) => { if (!el.dataset.pinned) hideHelp(el); });
  }, 250);
}
document.addEventListener("mouseover", (e) => {
  const icon = e.target.closest?.("[data-hi-help]");
  if (icon) { showHelp(icon); return; }
  if (e.target.closest?.(".hi-help-pop")) { clearTimeout(helpHide); return; }
});
document.addEventListener("mouseout", (e) => {
  if (e.target.closest?.("[data-hi-help], .hi-help-pop") && !e.relatedTarget?.closest?.("[data-hi-help], .hi-help-pop")) hideHelpSoon();
});
document.addEventListener("focusin", (e) => {
  const icon = e.target.closest?.("[data-hi-help]");
  if (icon) showHelp(icon);
});
document.addEventListener("focusout", (e) => {
  if (e.target.closest?.("[data-hi-help]") && !e.relatedTarget?.closest?.(".hi-help-pop")) hideHelpSoon();
});
document.addEventListener("click", (e) => {
  const icon = e.target.closest("[data-hi-help]");
  if (icon) {
    e.preventDefault();
    if (icon.dataset.pinned) { hideHelp(icon); return; }
    showHelp(icon);
    icon.dataset.pinned = "1";
    return;
  }
  if (!e.target.closest(".hi-help-pop")) document.querySelectorAll("[data-hi-help][aria-describedby]").forEach(hideHelp);
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") document.querySelectorAll("[data-hi-help][aria-describedby]").forEach(hideHelp);
});

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

// ---------------------------------------------------------------- writing assistant
// .hi-assist (partials/choices.html "write-assist"): "Enhance" sends the target textarea's
// current value to /assist/write; "Draft from campaign" sends none. Either way the result
// previews in place with Replace/Append/Discard; nothing touches the textarea until one of those
// is clicked, and nothing is saved until the surrounding form is submitted.
function assistUpdateButtons(root) {
  (root || document).querySelectorAll(".hi-assist").forEach((box) => {
    const ta = document.getElementById(box.dataset.assistTarget || "");
    const btn = box.querySelector("[data-assist-write]");
    if (btn) btn.disabled = !ta || !ta.value.trim();
  });
}
document.addEventListener("input", (e) => {
  if (e.target.matches?.("textarea, input")) assistUpdateButtons(document);
});
document.addEventListener("DOMContentLoaded", () => assistUpdateButtons(document));
document.addEventListener("htmx:afterSettle", () => assistUpdateButtons(document));

document.addEventListener("click", (e) => {
  const go = e.target.closest("[data-assist-write], [data-assist-draft]");
  if (go) {
    const box = go.closest(".hi-assist");
    const out = box && box.querySelector(".hi-assist-out");
    if (!box || !out || !window.htmx) return;
    const draft = go.hasAttribute("data-assist-draft");
    const ta = document.getElementById(box.dataset.assistTarget || "");
    go.disabled = true;
    out.innerHTML = '<span class="small text-body-secondary"><span class="spinner-border spinner-border-sm"></span> Thinking…</span>';
    htmx.ajax("POST", "/assist/write", {
      target: out, swap: "innerHTML",
      values: {
        field: box.dataset.assistField || "", mode: draft ? "draft" : "enhance",
        text: draft ? "" : (ta ? ta.value : ""), target: box.dataset.assistTarget || "",
        agent_id: box.dataset.assistAgent || "0", campaign_id: box.dataset.assistCampaign || "0",
        session_id: box.dataset.assistSession || "0",
      },
    }).finally(() => { go.disabled = false; assistUpdateButtons(box); });
    return;
  }
  const apply = e.target.closest("[data-assist-apply]");
  if (!apply) return;
  const box = apply.closest(".hi-assist");
  const out = box && box.querySelector(".hi-assist-out");
  if (apply.dataset.assistApply !== "discard") {
    const ta = box && document.getElementById(box.dataset.assistTarget || "");
    const text = apply.closest(".hi-assist-result")?.querySelector("p")?.textContent || "";
    if (ta && text) {
      ta.value = apply.dataset.assistApply === "append" && ta.value.trim() ? ta.value.replace(/\s+$/, "") + "\n\n" + text : text;
      ta.dispatchEvent(new Event("input", { bubbles: true }));
      ta.focus();
    }
  }
  if (out) out.innerHTML = "";
});
