// Picker UI for "Stash Files from Page".
//
// Lifecycle:
//   1. Background script opens picker.html?token=<one-shot> after
//      staging a placeholder in chrome.storage.session.
//   2. We read the token, render a loading state, then poll the
//      session storage until the background script finishes
//      `fetch_url_list`.
//   3. User picks; we send `fetch_url_pick` back through the
//      service worker via chrome.runtime.sendMessage.
//   4. On success we show a confirmation; the user can close the
//      popup manually.

const params = new URLSearchParams(location.search);
const token = params.get("token");

const statusEl = document.getElementById("status");
const candidatesEl = document.getElementById("candidates");
const pageInfoEl = document.getElementById("page-info");
const filterEl = document.getElementById("filter");
const archiveModeEl = document.getElementById("archive-mode");
const linkSourceEl = document.getElementById("link-source");
const stashBtn = document.getElementById("stash");
const cancelBtn = document.getElementById("cancel");
const selectedCountEl = document.getElementById("selected-count");

let pageURL = "";
let pageTitle = "";
let allCandidates = [];

// Track issued object URLs so we can revoke them on re-render and
// avoid leaking memory across filter passes / candidate refresh.
const objectURLs = new Set();
function clearObjectURLs() {
  for (const u of objectURLs) URL.revokeObjectURL(u);
  objectURLs.clear();
}

// Lazy-load thumbnails when they scroll into view. The page may
// have 50+ image candidates — fetching everything up front would
// waste bandwidth and slow first paint. The observer flips each
// thumb's data-thumb-url into an authenticated fetch only when
// the row is visible.
const thumbObserver = new IntersectionObserver(
  (entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      const el = entry.target;
      thumbObserver.unobserve(el);
      const url = el.dataset.thumbUrl;
      if (url) loadThumb(el, url);
    }
  },
  { rootMargin: "200px 0px" } // start loading slightly before scroll
);

async function loadThumb(thumbEl, url) {
  try {
    // Route through the service worker — extension pages still run
    // cross-origin fetches through CORS preflight even with
    // `<all_urls>` host_permissions, and most image CDNs reject the
    // preflight. The service worker's `fetch` bypasses CORS for any
    // host the extension has permission for.
    const resp = await chrome.runtime.sendMessage({
      type: "fetch_thumb",
      url,
    });
    if (!resp || !resp.ok || !resp.dataURL) {
      throw new Error(resp?.error || "fetch failed");
    }
    const img = document.createElement("img");
    img.src = resp.dataURL;
    img.referrerPolicy = "no-referrer";
    thumbEl.innerHTML = "";
    thumbEl.appendChild(img);
  } catch (err) {
    // Keep the 🖼 placeholder that's already in the DOM. The
    // real download via stash_blob may still succeed at Stash
    // time even if the thumb preview can't render here.
  }
}

(async function init() {
  if (!token) {
    setStatus("Missing picker token. Close this window and try again.", "error");
    return;
  }
  setStatus("Scanning page for files…", "loading");

  // Poll storage for the background script's response. Gives up
  // after ~30s — page scrapes shouldn't ever take that long.
  for (let i = 0; i < 60; i++) {
    const data = await chrome.storage.session.get(token);
    const state = data[token];
    if (state) {
      if (state.state === "ready") {
        renderReady(state);
        return;
      }
      if (state.state === "error") {
        setStatus("Failed to read page: " + state.error, "error");
        return;
      }
      // Still loading — keep page metadata visible early.
      if (state.pageURL) {
        pageInfoEl.textContent = state.pageTitle
          ? state.pageTitle + " — " + state.pageURL
          : state.pageURL;
      }
    }
    await sleep(500);
  }
  setStatus("Timed out waiting for the page scan.", "error");
})();

function renderReady(state) {
  pageURL = state.pageURL;
  pageTitle = state.pageTitle || "";
  allCandidates = state.candidates || [];

  pageInfoEl.textContent = pageTitle ? `${pageTitle} — ${pageURL}` : pageURL;

  if (allCandidates.length === 0) {
    setStatus("No file candidates found on this page.", "error");
    return;
  }

  setStatus(`${allCandidates.length} candidates found.`, "");
  renderList();
}

function renderList() {
  const filter = filterEl.value.trim().toLowerCase();
  // Revoke any object URLs we issued in a previous render so
  // filter / re-discover passes don't leak memory.
  clearObjectURLs();
  candidatesEl.innerHTML = "";

  for (const cand of allCandidates) {
    if (filter) {
      const hay = ((cand.label || "") + " " + cand.url).toLowerCase();
      if (!hay.includes(filter)) continue;
    }

    const li = document.createElement("li");
    li.className = "cand";

    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.dataset.url = cand.url;
    cb.addEventListener("change", updateSelectedCount);

    const thumb = document.createElement("div");
    thumb.className = "thumb";
    if (cand.kind === "image") {
      // Direct <img src=...> fails on hot-link-blocking CDNs like
      // lh3.googleusercontent.com — they 403 without the right
      // Referer/cookies. Use authenticated fetch (same path
      // stash_blob uses to actually grab the file) and feed an
      // object URL into the <img>. Lazy-loaded via IO so a
      // 50-image page doesn't fetch everything up front.
      thumb.innerHTML = "<span class=\"placeholder\">🖼</span>";
      thumb.dataset.thumbUrl = cand.url;
      thumbObserver.observe(thumb);
    } else {
      const ph = document.createElement("span");
      ph.className = "placeholder";
      ph.textContent = "📄";
      thumb.appendChild(ph);
    }

    const body = document.createElement("div");
    body.className = "body";
    const label = document.createElement("div");
    label.className = "label";
    label.textContent = cand.label || cand.url;
    const url = document.createElement("div");
    url.className = "url";
    url.textContent = cand.url;
    const badge = document.createElement("span");
    badge.className = "kind-badge";
    badge.textContent = cand.kind;
    label.appendChild(badge);
    body.appendChild(label);
    body.appendChild(url);

    li.appendChild(cb);
    li.appendChild(thumb);
    li.appendChild(body);

    // Whole-row click toggles the checkbox so clicking the label
    // works the same as clicking the box.
    li.addEventListener("click", (ev) => {
      if (ev.target !== cb) {
        cb.checked = !cb.checked;
        updateSelectedCount();
      }
    });

    candidatesEl.appendChild(li);
  }
  updateSelectedCount();
}

filterEl.addEventListener("input", renderList);

document.getElementById("select-all").addEventListener("click", () => {
  for (const cb of candidatesEl.querySelectorAll("input[type=checkbox]")) cb.checked = true;
  updateSelectedCount();
});
document.getElementById("select-none").addEventListener("click", () => {
  for (const cb of candidatesEl.querySelectorAll("input[type=checkbox]")) cb.checked = false;
  updateSelectedCount();
});
document.getElementById("select-images").addEventListener("click", () => {
  for (const cb of candidatesEl.querySelectorAll("input[type=checkbox]")) {
    const li = cb.closest(".cand");
    const isImage = li.querySelector(".kind-badge").textContent === "image";
    cb.checked = isImage;
  }
  updateSelectedCount();
});

cancelBtn.addEventListener("click", () => window.close());

stashBtn.addEventListener("click", async () => {
  const picks = pickedURLs();
  if (picks.length === 0) return;

  stashBtn.disabled = true;
  stashBtn.textContent = "Stashing…";
  setStatus(`Downloading ${picks.length} file${picks.length === 1 ? "" : "s"}…`, "loading");

  // Per-URL fetch path: the extension does the HTTP fetch (so it
  // has access to host permissions + session cookies for the
  // origin), then ships the bytes to the native host. Bypasses the
  // 403s the native host's anonymous HTTP gets on auth-gated CDNs
  // like Gemini's chat attachments.
  let imported = 0;
  const errors = [];

  if (archiveModeEl.checked) {
    // TODO: archive mode currently still uses the native-host
    // fetch path. Auth-gated bundles would need a multi-blob
    // shipping protocol; for now archive falls back to the
    // host-side fetch and may 403 on some CDNs.
    try {
      const resp = await chrome.runtime.sendMessage({
        type: "native",
        payload: {
          action: "fetch_url_pick",
          url: pageURL,
          picks,
          link_source: linkSourceEl.checked ? pageURL : "",
          archive: true,
        },
      });
      if (!resp || !resp.ok) {
        setStatus("Failed: " + (resp?.error || "unknown error"), "error");
        stashBtn.disabled = false;
        stashBtn.textContent = "Stash";
        return;
      }
      imported = (resp.imported || []).length;
    } catch (err) {
      setStatus("Failed: " + err.message, "error");
      stashBtn.disabled = false;
      stashBtn.textContent = "Stash";
      return;
    }
  } else {
    // Individual mode: per-URL extension fetch + base64 ship.
    // Look up the candidate's label so the stashed item gets a
    // human-readable title (alt text, link text, page-derived)
    // instead of the URL basename — for auth-gated CDN URLs the
    // basename is just a token like AEir0wL...
    const labelByURL = {};
    for (const c of allCandidates) labelByURL[c.url] = c.label || "";

    for (let i = 0; i < picks.length; i++) {
      const url = picks[i];
      setStatus(`Downloading (${i + 1}/${picks.length})…`, "loading");
      try {
        let title = labelByURL[url] || "";
        // Suffix with index when many picks share the same label
        // (common for "image" alt text on rendered chats).
        if (picks.length > 1 && title) {
          title = `${title} (${i + 1}/${picks.length})`;
        }
        // Fall back to page title with index if no label.
        if (!title && pageTitle) {
          title = `${pageTitle} (${i + 1}/${picks.length})`;
        }
        const resp = await chrome.runtime.sendMessage({
          type: "fetch_and_stash",
          url,
          referer: pageURL,
          link_source: linkSourceEl.checked ? pageURL : "",
          title,
        });
        if (resp && resp.ok) {
          imported++;
        } else {
          errors.push(`${url}: ${resp?.error || "unknown"}`);
        }
      } catch (err) {
        errors.push(`${url}: ${err.message}`);
      }
    }
  }

  let msg = `Stashed ${imported} item${imported === 1 ? "" : "s"}.`;
  if (errors.length > 0) {
    msg += ` ${errors.length} failed.`;
  }
  setStatus(msg, errors.length === 0 ? "success" : "error");
  if (errors.length > 0) {
    // Append the first few error lines under the status row so
    // the user can see which URLs failed without overflowing the
    // window. Full list is logged to the service worker console.
    const detail = document.createElement("div");
    detail.className = "status error";
    detail.style.whiteSpace = "pre-wrap";
    detail.style.maxHeight = "120px";
    detail.style.overflowY = "auto";
    detail.textContent = errors.slice(0, 6).join("\n") +
      (errors.length > 6 ? `\n…and ${errors.length - 6} more` : "");
    statusEl.parentElement.insertBefore(detail, statusEl.nextSibling);
    console.error("Stash errors:", errors);
  }
  stashBtn.textContent = imported > 0 ? "Done" : "Retry";
  stashBtn.disabled = imported > 0; // re-enable on full failure so user can retry

  if (imported > 0) {
    chrome.storage.session.remove(token);
    // Auto-close on full success so the user gets clear feedback
    // (popup vanishes) without having to hunt for a Done button.
    // Errors keep the window open so the failure rows are visible.
    if (errors.length === 0) {
      setStatus(`Stashed ${imported} item${imported === 1 ? "" : "s"}. Closing…`, "success");
      setTimeout(() => window.close(), 800);
    }
  }
});

function pickedURLs() {
  return Array.from(candidatesEl.querySelectorAll("input[type=checkbox]:checked"))
    .map((cb) => cb.dataset.url);
}

function updateSelectedCount() {
  const n = pickedURLs().length;
  selectedCountEl.textContent = `${n} selected`;
  stashBtn.disabled = n === 0;
}

function setStatus(text, cls) {
  statusEl.textContent = text;
  statusEl.className = "status " + (cls || "");
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}
