// Snippet picker for "Stash Selected Text". Two modes:
//
//  - New: create a fresh snippet item from the selection. Mirrors
//    the simple stash_text path but with optional title / tags /
//    collection up front.
//  - Append: search the existing stash, pick an item, append the
//    selection to its notes with an attribution header.
//
// Page → snippet payload is staged in chrome.storage.session by
// background.js the same way picker.html receives its candidate
// list — keyed by a one-shot token in the URL.

const params = new URLSearchParams(location.search);
const token = params.get("token");

// Module state
let pageURL = "";
let pageTitle = "";
let selection = "";
let pickedItem = null; // {id, title, type, url} when an existing item is selected
let searchTimer = null;

// DOM refs
const pageInfoEl = document.getElementById("page-info");
const selectionEl = document.getElementById("selection-text");
const newSection = document.getElementById("new-section");
const appendSection = document.getElementById("append-section");
const newTitleEl = document.getElementById("new-title");
const newTagsEl = document.getElementById("new-tags");
const newCollectionEl = document.getElementById("new-collection");
const searchEl = document.getElementById("search");
const resultsEl = document.getElementById("search-results");
const pickedEl = document.getElementById("picked-item");
const pickedTitleEl = pickedEl.querySelector(".picked-title");
const statusEl = document.getElementById("status");
const stashBtn = document.getElementById("stash");
const cancelBtn = document.getElementById("cancel");

// Initialize: pull the staged payload, render the selection, kick
// off a tag/collection refresh for the New mode dropdown.
(async function init() {
  if (!token) {
    setStatus("Missing snippet token. Close this window and try again.", "error");
    return;
  }
  const data = await chrome.storage.session.get(token);
  const stash = data[token];
  if (!stash) {
    setStatus("Snippet payload not found. Re-trigger from the page.", "error");
    return;
  }
  pageURL = stash.pageURL || "";
  pageTitle = stash.pageTitle || "";
  selection = stash.selection || "";

  pageInfoEl.textContent = pageTitle ? `${pageTitle} — ${pageURL}` : pageURL;
  selectionEl.textContent = selection;
  // Default title for New mode = page title; user can edit.
  newTitleEl.placeholder = pageTitle
    ? `(default: "${pageTitle}")`
    : "(default: source page title)";

  await loadCollections();
  searchEl.focus();
  // Pre-load recent items so the Append list is useful before
  // the user types — covers the "I just stashed something,
  // append a note to it" flow.
  runSearch("");
})();

// Mode toggle: show only the section for the active mode.
for (const radio of document.querySelectorAll('input[name="mode"]')) {
  radio.addEventListener("change", () => {
    const mode = currentMode();
    newSection.classList.toggle("hidden", mode !== "new");
    appendSection.classList.toggle("hidden", mode !== "append");
    updateStashEnabled();
    if (mode === "append") searchEl.focus();
  });
}

function currentMode() {
  return document.querySelector('input[name="mode"]:checked').value;
}

// --- Search (Append mode) ---

searchEl.addEventListener("input", () => {
  clearTimeout(searchTimer);
  const q = searchEl.value.trim();
  // Debounce so we don't fire a native message on every keystroke.
  // Empty query is intentionally allowed — it falls through to
  // ListItems on the native side, which returns recent items
  // newest-first. Common case: the user just stashed an image
  // and wants to append a snippet to it; the top of the list is
  // almost always what they're looking for.
  searchTimer = setTimeout(() => runSearch(q), q ? 200 : 0);
});

// Focusing the search field with no query in flight also loads
// the recent-items list so the user doesn't have to type just to
// see anything.
searchEl.addEventListener("focus", () => {
  if (!searchEl.value.trim() && resultsEl.children.length === 0) {
    runSearch("");
  }
});

async function runSearch(query) {
  try {
    const resp = await chrome.runtime.sendMessage({
      type: "native",
      payload: { action: "search", query, limit: 8 },
    });
    if (resp && resp.ok && resp.items) {
      renderResults(resp.items);
    } else {
      resultsEl.innerHTML = "";
    }
  } catch (err) {
    console.error("search error:", err);
  }
}

function renderResults(items) {
  resultsEl.innerHTML = "";
  for (const item of items) {
    const li = document.createElement("li");
    li.className = "search-row";
    if (pickedItem && pickedItem.id === item.id) li.classList.add("active");

    const icon = document.createElement("span");
    icon.className = "icon";
    icon.textContent = typeGlyph(item.type);

    const body = document.createElement("div");
    body.className = "body";
    const title = document.createElement("div");
    title.className = "title";
    title.textContent = item.title || "(untitled)";
    const meta = document.createElement("div");
    meta.className = "meta";
    meta.textContent = item.url || item.type;
    body.appendChild(title);
    body.appendChild(meta);

    li.appendChild(icon);
    li.appendChild(body);
    li.addEventListener("click", () => {
      pickedItem = {
        id: item.id,
        title: item.title || "(untitled)",
        type: item.type,
        url: item.url,
      };
      pickedTitleEl.textContent = pickedItem.title;
      pickedEl.classList.remove("hidden");
      // Highlight just the picked row.
      for (const row of resultsEl.children) row.classList.remove("active");
      li.classList.add("active");
      updateStashEnabled();
    });
    resultsEl.appendChild(li);
  }
}

function typeGlyph(type) {
  switch (type) {
    case "link":    return "🔗";
    case "image":   return "🖼";
    case "file":    return "📄";
    case "snippet": return "✍️";
    case "email":   return "✉️";
    default:        return "•";
  }
}

// --- Collections (New mode) ---

async function loadCollections() {
  try {
    const resp = await chrome.runtime.sendMessage({
      type: "native",
      payload: { action: "list_collections" },
    });
    if (resp && resp.ok && resp.collections) {
      for (const c of resp.collections) {
        const opt = document.createElement("option");
        opt.value = c.name;
        opt.textContent = c.name;
        newCollectionEl.appendChild(opt);
      }
    }
  } catch {
    // List failures are non-fatal — the user can type-in the
    // collection name later via the Mac app or CLI.
  }
}

// --- Stash button ---

stashBtn.addEventListener("click", async () => {
  stashBtn.disabled = true;
  stashBtn.textContent = "Stashing…";
  setStatus("");

  try {
    if (currentMode() === "new") {
      await stashAsNew();
    } else {
      await stashAsAppend();
    }
  } catch (err) {
    setStatus("Failed: " + err.message, "error");
    stashBtn.disabled = false;
    stashBtn.textContent = "Stash";
  }
});

async function stashAsNew() {
  const title = newTitleEl.value.trim() || pageTitle || "Selection";
  const tags = newTagsEl.value
    .split(",")
    .map((t) => t.trim())
    .filter(Boolean);
  const collection = newCollectionEl.value;

  const resp = await chrome.runtime.sendMessage({
    type: "native",
    payload: {
      action: "stash_text",
      text: selection,
      title,
      tags,
      collection,
      url: pageURL, // source attribution
    },
  });
  if (!resp || !resp.ok) throw new Error(resp?.error || "unknown error");
  setStatus("Stashed as new snippet. Closing…", "success");
  chrome.storage.session.remove(token);
  setTimeout(() => window.close(), 800);
}

async function stashAsAppend() {
  if (!pickedItem) throw new Error("Pick an item first");
  const resp = await chrome.runtime.sendMessage({
    type: "native",
    payload: {
      action: "append_notes",
      item_id: pickedItem.id,
      text: selection,
      source_url: pageURL,
      source_title: pageTitle,
    },
  });
  if (!resp || !resp.ok) throw new Error(resp?.error || "unknown error");
  setStatus(`Appended to "${pickedItem.title}". Closing…`, "success");
  chrome.storage.session.remove(token);
  setTimeout(() => window.close(), 800);
}

// --- UI helpers ---

cancelBtn.addEventListener("click", () => window.close());

function updateStashEnabled() {
  if (currentMode() === "append") {
    stashBtn.disabled = !pickedItem;
  } else {
    stashBtn.disabled = false;
  }
}

function setStatus(text, cls) {
  statusEl.textContent = text;
  statusEl.className = "status " + (cls || "");
}

// Ensure initial button state is right.
updateStashEnabled();
