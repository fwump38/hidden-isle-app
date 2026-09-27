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
