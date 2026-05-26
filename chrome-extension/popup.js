const pageTitle = document.getElementById("page-title");
const pageUrl = document.getElementById("page-url");
const status = document.getElementById("status");
const tagsInput = document.getElementById("tags");
const notesInput = document.getElementById("notes");
const collectionSelect = document.getElementById("collection");
const stashBtn = document.getElementById("stash-btn");
const message = document.getElementById("message");
const tagDropdown = document.getElementById("tag-dropdown");
const searchInput = document.getElementById("search-input");
const searchResults = document.getElementById("search-results");
const stashView = document.getElementById("stash-view");
const searchView = document.getElementById("search-view");
const tagCloud = document.getElementById("tag-cloud");
const searchClear = document.getElementById("search-clear");
const snippetPreview = document.getElementById("snippet-preview");
const snippetText = document.getElementById("snippet-text");

const searchTagDropdown = document.getElementById("search-tag-dropdown");
const modeBtn = document.getElementById("mode-btn");

// View toggle (cloud / recent / frequent). One is visible at a
// time when the search input is empty; typing into the input
// switches to live search results regardless of which view is
// currently active.
const viewToggle = document.getElementById("view-toggle");
const recentList = document.getElementById("recent-list");
const frequentList = document.getElementById("frequent-list");
const VIEWS = ["cloud", "recent", "frequent"];
const VIEW_LABELS = {
  cloud:    "tag cloud",
  recent:   "recent",
  frequent: "frequent",
};
let currentView = "cloud";

let currentTab = null;
let allTags = [];
let activeIndex = -1;
let searchTagActiveIndex = -1;
let existingItem = null;
let searchTimer = null;
let selectedText = "";
let tagJustAccepted = false;
let searchTagJustAccepted = false;

// --- View toggle ---

function switchTab(target) {
  stashView.classList.toggle("hidden", target !== "stash");
  searchView.classList.toggle("hidden", target !== "search");

  if (target === "search") {
    modeBtn.classList.remove("back");
    modeBtn.title = "Add to your Stash";
    modeBtn.setAttribute("aria-label", "Add to your Stash");
    searchInput.focus();
    if (searchInput.value.trim().length === 0) {
      showBrowseView();
    }
  } else {
    modeBtn.classList.add("back");
    modeBtn.title = "Search your Stash";
    modeBtn.setAttribute("aria-label", "Search your Stash");
  }
}

modeBtn.addEventListener("click", () => {
  const inSearch = !searchView.classList.contains("hidden");
  switchTab(inSearch ? "stash" : "search");
});

// --- Init ---

async function init() {
  // Restore the last browse view so a returning user lands on
  // whatever they were using (cloud / recent / frequent).
  // Defaults to "cloud" if there's no saved preference yet.
  try {
    const stored = await chrome.storage.local.get(["stashLastBrowseView"]);
    if (stored.stashLastBrowseView && VIEWS.includes(stored.stashLastBrowseView)) {
      currentView = stored.stashLastBrowseView;
    }
  } catch {
    // Storage is unavailable in some sandboxed contexts — fall
    // through with the default.
  }

  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  currentTab = tab;
  // Render any per-site routing toggles applicable to this tab so
  // the user can pause auto-stash before doing a non-stash
  // download from a matched site.
  renderRoutingToggles(tab).catch(() => {});

  pageTitle.textContent = tab.title || "Untitled";
  pageUrl.textContent = tab.url || "";

  // Run DOM scraper to detect email metadata (captured_at)
  try {
    const scrape = await chrome.runtime.sendMessage({ type: "scrape_dom", tab });
    if (scrape && scrape.page_captured_at) {
      tab.captured_at = scrape.page_captured_at;
      status.textContent = "Email detected: " + new Date(scrape.page_captured_at).toLocaleString();
    }
  } catch (err) {
    // Non-fatal if scraper fails
  }

  // Grab selected text from the active tab
  try {
    if (tab.url && (tab.url.startsWith("http://") || tab.url.startsWith("https://"))) {
      const results = await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        func: () => window.getSelection().toString(),
      });
      if (results && results[0] && results[0].result) {
        selectedText = results[0].result.trim();
        if (selectedText.length > 0) {
          snippetPreview.classList.remove("hidden");
          snippetText.textContent =
            selectedText.length > 300
              ? selectedText.substring(0, 300) + "..."
              : selectedText;
          stashBtn.textContent = "Stash Selection";
        }
      }
    }
  } catch {
    // Content script injection may fail on restricted pages
  }

  // Check if already stashed
  try {
    const resp = await sendMessage({ action: "check_url", url: tab.url });
    if (resp.ok && resp.exists) {
      existingItem = resp.item;
      status.textContent = "Already stashed";
      status.className = "exists";
      stashBtn.textContent = "Update";

      // Pre-fill with existing data
      if (resp.item) {
        if (resp.item.tags && resp.item.tags.length > 0) {
          tagsInput.value = resp.item.tags.map((t) => t.name).join(", ");
        }
        if (resp.item.notes) {
          notesInput.value = resp.item.notes;
        }
      }
    }
  } catch {
    showMessage("Cannot connect to stash. Is the native host installed?", "error");
    stashBtn.disabled = true;
    return;
  }

  // Load tags for autocomplete
  try {
    const resp = await sendMessage({ action: "list_tags" });
    if (resp.ok && resp.tags) {
      allTags = resp.tags;
    }
  } catch {}

  // Load collections
  try {
    const resp = await sendMessage({ action: "list_collections" });
    if (resp.ok && resp.collections) {
      resp.collections.forEach((col) => {
        const option = document.createElement("option");
        option.value = col.name;
        option.textContent = col.name;
        collectionSelect.appendChild(option);
      });
    }
  } catch {}

  switchTab("search");
}

// --- Tag Autocomplete ---

function getCurrentTag() {
  const val = tagsInput.value;
  const cursor = tagsInput.selectionStart;
  const before = val.substring(0, cursor);
  const lastComma = before.lastIndexOf(",");
  return before.substring(lastComma + 1).trim();
}

function getExistingTags() {
  // Skip the tag at the cursor — it's mid-typed and shouldn't
  // be filtered out of its own suggestion list. Tags are
  // comma-separated; the cursor's current token is the segment
  // between the last comma before the cursor and the next comma
  // (or end of string). All other segments count as "already used".
  const val = tagsInput.value;
  const cursor = tagsInput.selectionStart;
  // Walk through the comma-separated segments, tracking each
  // segment's [start, end] range in the raw string.
  const result = [];
  let segmentStart = 0;
  for (let i = 0; i <= val.length; i++) {
    if (i === val.length || val[i] === ",") {
      const segmentEnd = i;
      const inCursorRange = cursor >= segmentStart && cursor <= segmentEnd;
      if (!inCursorRange) {
        const trimmed = val.substring(segmentStart, segmentEnd).trim().toLowerCase();
        if (trimmed) result.push(trimmed);
      }
      segmentStart = i + 1;
    }
  }
  return result;
}

function updateDropdown() {
  const current = getCurrentTag().toLowerCase();
  if (current.length === 0) {
    hideDropdown();
    return;
  }

  const existing = getExistingTags();
  const matches = allTags.filter(
    (t) =>
      t.name.toLowerCase().includes(current) &&
      !existing.includes(t.name.toLowerCase())
  );

  if (matches.length === 0) {
    hideDropdown();
    return;
  }

  tagDropdown.innerHTML = "";
  activeIndex = -1;

  matches.slice(0, 8).forEach((tag, i) => {
    const div = document.createElement("div");
    div.className = "tag-option";
    div.innerHTML = `<span class="tag-name">${escapeHtml(tag.name)}</span><span class="tag-count">${tag.count || 0}</span>`;
    div.addEventListener("mousedown", (e) => {
      e.preventDefault();
      selectTag(tag.name);
    });
    tagDropdown.appendChild(div);
  });

  tagDropdown.classList.remove("hidden");
}

function selectTag(name) {
  const val = tagsInput.value;
  const cursor = tagsInput.selectionStart;
  const before = val.substring(0, cursor);
  const after = val.substring(cursor);
  const lastComma = before.lastIndexOf(",");

  const prefix = lastComma >= 0 ? before.substring(0, lastComma + 1) + " " : "";
  const suffix = after.trimStart();
  const needsComma = suffix.length > 0 && !suffix.startsWith(",");

  tagsInput.value = prefix + name + (needsComma ? ", " : ", ") + suffix;
  tagsInput.focus();

  const newCursor = (prefix + name + ", ").length;
  tagsInput.setSelectionRange(newCursor, newCursor);

  hideDropdown();
}

function hideDropdown() {
  tagDropdown.classList.add("hidden");
  tagDropdown.innerHTML = "";
  activeIndex = -1;
}

tagsInput.addEventListener("focus", updateDropdown);
tagsInput.addEventListener("blur", () => {
  setTimeout(hideDropdown, 150);
});

tagsInput.addEventListener("keydown", (e) => {
  const options = tagDropdown.querySelectorAll(".tag-option");
  const dropdownVisible =
    !tagDropdown.classList.contains("hidden") && options.length > 0;

  if (dropdownVisible) {
    if (e.key === "Tab") {
      e.preventDefault();
      // First Tab selects index 0, subsequent Tabs advance
      activeIndex = activeIndex < 0 ? 0 : Math.min(activeIndex + 1, options.length - 1);
      updateActiveOption(options);
      // Preview the selection in the field
      previewTag(options[activeIndex].querySelector(".tag-name").textContent);
      return;
    }
    if (e.key === "ArrowDown" || (e.ctrlKey && e.code === "KeyJ")) {
      e.preventDefault();
      activeIndex = Math.min(activeIndex + 1, options.length - 1);
      updateActiveOption(options);
      previewTag(options[activeIndex].querySelector(".tag-name").textContent);
      return;
    }
    if (e.key === "ArrowUp" || (e.ctrlKey && e.code === "KeyK")) {
      e.preventDefault();
      activeIndex = Math.max(activeIndex - 1, 0);
      updateActiveOption(options);
      previewTag(options[activeIndex].querySelector(".tag-name").textContent);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      if (activeIndex >= 0) {
        // User explicitly picked a suggestion via Tab / Arrow / Ctrl-J/K.
        selectTag(options[activeIndex].querySelector(".tag-name").textContent);
      } else {
        // No active navigation — the user typed text that happens to match
        // a suggestion as a substring (e.g. "ra" substringing "programming").
        // Commit what they typed verbatim as a new tag, preserving the
        // comma-separated pattern for adding more.
        const typed = getCurrentTag();
        if (typed.length === 0) return;
        selectTag(typed);
      }
      tagJustAccepted = true;
      return;
    }
    if (e.key === "Escape") {
      hideDropdown();
      return;
    }
  }

  // Enter when no dropdown: if we just accepted a tag, consume the first Enter
  if (e.key === "Enter" && tagJustAccepted) {
    e.preventDefault();
    e.stopPropagation();
    tagJustAccepted = false;
    return;
  }
});

// Preview a tag in the field without committing (no trailing comma yet)
function previewTag(name) {
  const val = tagsInput.value;
  const cursor = tagsInput.selectionStart;
  const before = val.substring(0, cursor);
  const after = val.substring(cursor);
  const lastComma = before.lastIndexOf(",");

  const prefix = lastComma >= 0 ? before.substring(0, lastComma + 1) + " " : "";
  const suffix = after.trimStart();

  tagsInput.value = prefix + name + (suffix.length > 0 ? ", " + suffix : "");
  const newCursor = (prefix + name).length;
  tagsInput.setSelectionRange(newCursor, newCursor);
}

// Reset accept flag on any input
tagsInput.addEventListener("input", () => {
  tagJustAccepted = false;
  updateDropdown();
});

function updateActiveOption(options) {
  options.forEach((opt, i) => {
    opt.classList.toggle("active", i === activeIndex);
  });
  if (activeIndex >= 0 && options[activeIndex]) {
    options[activeIndex].scrollIntoView({ block: "nearest" });
  }
}

// --- Stash ---

stashBtn.addEventListener("click", async () => {
  if (!currentTab) return;

  stashBtn.disabled = true;
  stashBtn.textContent = existingItem ? "Updating..." : "Stashing...";

  const tags = tagsInput.value
    .split(",")
    .map((t) => t.trim())
    .filter(Boolean);

  const isSnippet = selectedText.length > 0 && !existingItem;
  const action = existingItem ? "update_url" : isSnippet ? "stash_text" : "stash_url";

  const payload = isSnippet
    ? {
        action,
        text: selectedText,
        title: currentTab.title + " (selection)",
        tags,
        notes: notesInput.value.trim(),
        collection: collectionSelect.value,
        source_url: currentTab.url,
        captured_at: currentTab.captured_at,
      }
    : {
        action,
        url: currentTab.url,
        title: currentTab.title,
        tags,
        notes: notesInput.value.trim(),
        collection: collectionSelect.value,
        captured_at: currentTab.captured_at,
      };

  try {
    const resp = await sendMessage(payload);

    if (resp.ok) {
      showMessage(existingItem ? "Updated!" : "Stashed!", "success");
      stashBtn.textContent = existingItem ? "Updated" : "Stashed";
      setTimeout(() => window.close(), 800);
    } else {
      showMessage(resp.error || "Failed to stash", "error");
      stashBtn.disabled = false;
      stashBtn.textContent = "Stash It";
    }
  } catch (err) {
    showMessage(err.message, "error");
    stashBtn.disabled = false;
    stashBtn.textContent = "Stash It";
  }
});

// --- Search ---

// Detect if the cursor is inside a "tag:" token being typed
function getSearchTagPartial() {
  const val = searchInput.value;
  const cursor = searchInput.selectionStart;
  const before = val.substring(0, cursor);
  const match = before.match(/(?:^|\s)tag:(\S*)$/);
  return match ? match[1] : null;
}

function getExistingSearchTags() {
  // Skip the tag at the cursor — the user is mid-typing it, so it
  // doesn't count as "already used" and should still appear in
  // the suggestion list (matching the project's autocomplete
  // contract: "the word currently being edited does not count as
  // 'used'"). Without this guard, typing `tag:ra` excludes the
  // exact-match tag named `ra` from its own suggestion list.
  const val = searchInput.value;
  const cursor = searchInput.selectionStart;
  const tags = [];
  for (const m of val.matchAll(/tag:(\S+)/g)) {
    const tokenStart = m.index;
    const tokenEnd = m.index + m[0].length;
    if (cursor >= tokenStart && cursor <= tokenEnd) continue;
    tags.push(m[1].toLowerCase());
  }
  return tags;
}

function updateSearchTagDropdown() {
  const partial = getSearchTagPartial();
  if (partial === null) {
    hideSearchTagDropdown();
    return;
  }

  const existing = getExistingSearchTags();
  const lowerPartial = partial.toLowerCase();
  const matches = allTags.filter(
    (t) =>
      t.name.toLowerCase().includes(lowerPartial) &&
      !existing.includes(t.name.toLowerCase())
  );

  if (matches.length === 0) {
    hideSearchTagDropdown();
    return;
  }

  searchTagDropdown.innerHTML = "";
  searchTagActiveIndex = -1;

  // Cap at 30 — enough to scroll through and find anything in
  // a library with ~hundreds of tags, but bounded so the dropdown
  // doesn't grow unbounded.
  matches.slice(0, 30).forEach((tag) => {
    const div = document.createElement("div");
    div.className = "tag-option";
    div.innerHTML = `<span class="tag-name">${escapeHtml(tag.name)}</span><span class="tag-count">${tag.count || 0}</span>`;
    div.addEventListener("mousedown", (e) => {
      e.preventDefault();
      selectSearchTag(tag.name);
    });
    searchTagDropdown.appendChild(div);
  });

  searchTagDropdown.classList.remove("hidden");
}

function selectSearchTag(name) {
  const val = searchInput.value;
  const cursor = searchInput.selectionStart;
  const before = val.substring(0, cursor);
  const after = val.substring(cursor);

  // Find the start of the "tag:" token
  const match = before.match(/(^|\s)tag:\S*$/);
  if (!match) return;

  const tokenStart = before.length - match[0].length + match[1].length;
  const prefix = val.substring(0, tokenStart);
  const suffix = after.replace(/^\S*/, ""); // consume any remaining partial after cursor

  searchInput.value = prefix + "tag:" + name + " " + suffix.trimStart();
  searchInput.focus();

  const newCursor = (prefix + "tag:" + name + " ").length;
  searchInput.setSelectionRange(newCursor, newCursor);

  hideSearchTagDropdown();

  // Trigger search with updated query
  clearTimeout(searchTimer);
  const query = searchInput.value.trim();
  if (query.length > 0) {
    hideBrowseViews();
    searchTimer = setTimeout(() => doSearch(query), 200);
  }
}

function hideSearchTagDropdown() {
  searchTagDropdown.classList.add("hidden");
  searchTagDropdown.innerHTML = "";
  searchTagActiveIndex = -1;
}

function updateActiveSearchTagOption() {
  const options = searchTagDropdown.querySelectorAll(".tag-option");
  options.forEach((opt, i) => {
    opt.classList.toggle("active", i === searchTagActiveIndex);
  });
  if (searchTagActiveIndex >= 0 && options[searchTagActiveIndex]) {
    options[searchTagActiveIndex].scrollIntoView({ block: "nearest" });
  }
}

searchInput.addEventListener("input", () => {
  searchTagJustAccepted = false;
  clearTimeout(searchTimer);
  const query = searchInput.value.trim();
  searchClear.classList.toggle("hidden", query.length === 0);

  // As soon as the field has any text, get the browse panes
  // (cloud / recent / frequent) out of the way. The bug fix: the
  // tag: autocomplete branch below used to return early, so the
  // history pane stayed stacked above live search results.
  if (query.length > 0) {
    hideBrowseViews();
  }

  // Check for tag: autocomplete first
  if (getSearchTagPartial() !== null) {
    updateSearchTagDropdown();
    // Still run the live search underneath the tag dropdown so
    // results refresh even while the user is mid-tag-completion.
    searchTimer = setTimeout(() => doSearch(query), 200);
    return;
  }
  hideSearchTagDropdown();

  if (query.length === 0) {
    showBrowseView();
    return;
  }
  searchTimer = setTimeout(() => doSearch(query), 200);
});

searchInput.addEventListener("keydown", (e) => {
  const options = searchTagDropdown.querySelectorAll(".tag-option");
  const dropdownVisible =
    !searchTagDropdown.classList.contains("hidden") && options.length > 0;

  if (dropdownVisible) {
    if (e.key === "Tab") {
      e.preventDefault();
      searchTagActiveIndex =
        searchTagActiveIndex < 0
          ? 0
          : Math.min(searchTagActiveIndex + 1, options.length - 1);
      updateActiveSearchTagOption();
      previewSearchTag(
        options[searchTagActiveIndex].querySelector(".tag-name").textContent
      );
      return;
    }
    if (e.key === "ArrowDown" || (e.ctrlKey && e.code === "KeyJ")) {
      e.preventDefault();
      searchTagActiveIndex = Math.min(
        searchTagActiveIndex + 1,
        options.length - 1
      );
      updateActiveSearchTagOption();
      previewSearchTag(
        options[searchTagActiveIndex].querySelector(".tag-name").textContent
      );
      return;
    }
    if (e.key === "ArrowUp" || (e.ctrlKey && e.code === "KeyK")) {
      e.preventDefault();
      searchTagActiveIndex = Math.max(searchTagActiveIndex - 1, 0);
      updateActiveSearchTagOption();
      previewSearchTag(
        options[searchTagActiveIndex].querySelector(".tag-name").textContent
      );
      return;
    }
    if (e.key === "Enter") {
      if (searchTagActiveIndex >= 0) {
        // User explicitly picked a suggestion via Tab / Arrow / Ctrl-J/K.
        e.preventDefault();
        selectSearchTag(
          options[searchTagActiveIndex].querySelector(".tag-name").textContent
        );
        searchTagJustAccepted = true;
        return;
      }
      // No active navigation — let the typed query stand and fall through
      // to whatever runs the search. Don't rewrite the user's text to a
      // substring-match they never asked for.
      hideSearchTagDropdown();
      // fallthrough to default Enter behavior
    }
    if (e.key === "Escape") {
      hideSearchTagDropdown();
      return;
    }
  }

  // Enter when no dropdown: if we just accepted a tag, consume the first Enter
  if (e.key === "Enter" && searchTagJustAccepted) {
    e.preventDefault();
    searchTagJustAccepted = false;
    return;
  }
});

// Preview a search tag in the field without committing
function previewSearchTag(name) {
  const val = searchInput.value;
  const cursor = searchInput.selectionStart;
  const before = val.substring(0, cursor);
  const after = val.substring(cursor);

  const match = before.match(/(^|\s)tag:\S*$/);
  if (!match) return;

  const tokenStart = before.length - match[0].length + match[1].length;
  const prefix = val.substring(0, tokenStart);
  const suffix = after.replace(/^\S*/, "");

  searchInput.value = prefix + "tag:" + name + suffix;
  const newCursor = (prefix + "tag:" + name).length;
  searchInput.setSelectionRange(newCursor, newCursor);
}

searchInput.addEventListener("blur", () => {
  setTimeout(hideSearchTagDropdown, 150);
});

searchClear.addEventListener("click", () => {
  searchInput.value = "";
  searchClear.classList.add("hidden");
  hideSearchTagDropdown();
  showBrowseView();
  searchInput.focus();
});

/// Hide all three browse-view containers (cloud / recent /
/// frequent). Called whenever a live search is about to render
/// results so the panes don't stack on top of each other.
function hideBrowseViews() {
  tagCloud.classList.add("hidden");
  recentList.classList.add("hidden");
  frequentList.classList.add("hidden");
}

/// Render the browse-view container that matches the current
/// view setting (cloud / recent / frequent). Hides the search-
/// results pane since the user just cleared the input.
function showBrowseView() {
  searchResults.innerHTML = "";
  // Hide all three, then unhide the one we want.
  tagCloud.classList.add("hidden");
  recentList.classList.add("hidden");
  frequentList.classList.add("hidden");
  switch (currentView) {
    case "recent":
      recentList.classList.remove("hidden");
      loadHistory("recent");
      break;
    case "frequent":
      frequentList.classList.remove("hidden");
      loadHistory("frequent");
      break;
    default:
      tagCloud.classList.remove("hidden");
      if (tagCloud.children.length === 0) loadTagCloud();
  }
  updateViewToggleUI();
}

/// Refresh the toggle button's icon + tooltip based on the
/// current view. Tooltip describes the NEXT view the click will
/// switch to, mirroring how the macOS Photos picker reads.
function updateViewToggleUI() {
  viewToggle.classList.remove("view-cloud", "view-recent", "view-frequent");
  viewToggle.classList.add("view-" + currentView);
  const i = VIEWS.indexOf(currentView);
  const next = VIEWS[(i + 1) % VIEWS.length];
  viewToggle.title = `Show ${VIEW_LABELS[next]}`;
  viewToggle.setAttribute("aria-label", `Show ${VIEW_LABELS[next]}`);
}

/// Cycle: cloud → recent → frequent → cloud. Persists the new
/// view to chrome.storage.local so the next popup open lands on
/// the same one.
viewToggle.addEventListener("click", () => {
  const i = VIEWS.indexOf(currentView);
  currentView = VIEWS[(i + 1) % VIEWS.length];
  chrome.storage.local.set({ stashLastBrowseView: currentView });
  showBrowseView();
});

/// Load Recent or Frequent rollup from the native host and
/// render it into the matching pane. Empty result → "No history
/// yet" placeholder.
async function loadHistory(sort) {
  const pane = sort === "frequent" ? frequentList : recentList;
  pane.innerHTML = `<div class="empty">Loading…</div>`;
  try {
    const resp = await sendMessage({
      action: "search_history_list",
      sort,
      limit: 30,
    });
    if (!resp || !resp.ok) {
      pane.innerHTML = `<div class="empty">${escapeHtml(resp?.error || "Failed to load")}</div>`;
      return;
    }
    const entries = resp.history || [];
    if (entries.length === 0) {
      pane.innerHTML = `<div class="empty">No saved searches yet — click a result to record one.</div>`;
      return;
    }
    pane.innerHTML = "";
    for (const entry of entries) {
      const row = document.createElement("div");
      row.className = "history-row";
      row.innerHTML =
        `<span class="row-query"></span>` +
        `<span class="row-meta">${entry.count}×</span>`;
      row.querySelector(".row-query").textContent = entry.query;
      row.addEventListener("click", () => {
        // Click on a history row = re-run that search. Repopulate
        // the input, then dispatch the same search path live
        // search uses. Won't double-record — the record happens
        // only when the user clicks a RESULT, not a history row.
        searchInput.value = entry.query;
        searchClear.classList.remove("hidden");
        hideBrowseViews();
        doSearch(entry.query);
      });
      pane.appendChild(row);
    }
  } catch (err) {
    pane.innerHTML = `<div class="empty">${escapeHtml(err.message)}</div>`;
  }
}

async function loadRecent() {
  try {
    // No limit — show every match. The popup pane scrolls if the
    // list is long; the user explicitly opted out of capping.
    const resp = await sendMessage({ action: "search", query: "", limit: 100000 });
    if (resp.ok) {
      renderResults(resp.items || [], "Recent items");
    }
  } catch {}
}

async function doSearch(query) {
  try {
    const resp = await sendMessage({ action: "search", query, limit: 100000 });
    if (resp.ok) {
      renderResults(resp.items || [], "No results found");
    }
  } catch (err) {
    searchResults.innerHTML = `<div class="empty">${escapeHtml(err.message)}</div>`;
  }
}

function renderResults(items, emptyText) {
  searchResults.innerHTML = "";

  if (items.length === 0) {
    searchResults.innerHTML = `<div class="empty">${escapeHtml(emptyText)}</div>`;
    return;
  }

  items.forEach((item) => {
    const el = document.createElement("a");
    el.className = "search-item";
    el.href = item.url || "#";
    el.addEventListener("click", (e) => {
      e.preventDefault();
      // Record the search criteria at click time — the user has
      // committed to this result, so the query in the field is
      // worth remembering for Recent / Frequent. Fire-and-forget;
      // failure is non-fatal and we never want to block opening
      // the link.
      const committed = searchInput.value.trim();
      if (committed) {
        sendMessage({
          action: "search_history_record",
          query: committed,
          item_id: item.id,
        }).catch(() => {});
      }
      if (item.url) {
        chrome.tabs.create({ url: item.url });
        window.close();
      }
    });

    let html = `<span class="search-item-title">${escapeHtml(item.title)}`;
    if (item.type && item.type !== "link") {
      html += `<span class="search-item-type">${escapeHtml(item.type)}</span>`;
    }
    html += `</span>`;

    if (item.url) {
      html += `<span class="search-item-url">${escapeHtml(item.url)}</span>`;
    }

    if (item.tags && item.tags.length > 0) {
      html += `<span class="search-item-tags">`;
      item.tags.forEach((t) => {
        html += `<span class="search-item-tag">${escapeHtml(t.name)}</span>`;
      });
      html += `</span>`;
    }

    el.innerHTML = html;
    searchResults.appendChild(el);
  });
}

// --- Tag Cloud ---

async function loadTagCloud() {
  try {
    const resp = await sendMessage({ action: "list_tags" });
    if (resp.ok && resp.tags && resp.tags.length > 0) {
      renderTagCloud(resp.tags);
    } else {
      tagCloud.innerHTML = '<div class="empty">No tags yet</div>';
    }
  } catch (err) {
    tagCloud.innerHTML = `<div class="empty">${escapeHtml(err.message)}</div>`;
  }
}

function renderTagCloud(tags) {
  tagCloud.innerHTML = "";

  // Sort alphabetically
  const sorted = [...tags].sort((a, b) => a.name.localeCompare(b.name));

  // Compute size tiers based on count distribution
  const counts = sorted.map((t) => t.count || 0);
  const maxCount = Math.max(...counts);
  const minCount = Math.min(...counts);
  const range = maxCount - minCount || 1;

  sorted.forEach((tag, i) => {
    const el = document.createElement("span");
    const tier = Math.ceil(((tag.count - minCount) / range) * 4) + 1;
    el.className = `cloud-tag size-${Math.min(tier, 5)}`;
    el.textContent = tag.name;
    el.addEventListener("click", () => {
      searchInput.value = `tag:${tag.name}`;
      searchClear.classList.remove("hidden");
      hideBrowseViews();
      doSearch(`tag:${tag.name}`);
    });
    tagCloud.appendChild(el);

    if (i < sorted.length - 1) {
      const sep = document.createElement("span");
      sep.className = "cloud-sep";
      sep.textContent = ", ";
      tagCloud.appendChild(sep);
    }
  });
}

// --- Helpers ---

function sendMessage(payload) {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage({ type: "native", payload }, (response) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
      } else {
        resolve(response);
      }
    });
  });
}

function showMessage(text, type) {
  message.textContent = text;
  message.className = type;
}

function escapeHtml(str) {
  if (!str) return "";
  return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// Keyboard: Enter to stash (only when no dropdown active and on stash view)
document.addEventListener("keydown", (e) => {
  if (
    e.key === "Enter" &&
    !e.shiftKey &&
    !stashBtn.disabled &&
    activeIndex < 0 &&
    !tagJustAccepted &&
    !stashView.classList.contains("hidden") &&
    tagDropdown.classList.contains("hidden")
  ) {
    stashBtn.click();
  }
});

// Per-site routing toggles. Reads the download-rules list from
// chrome.storage.sync, finds the subset that matches the active
// tab's host, and renders one checkbox per match in the popup's
// routing section. Toggling persists immediately back to storage;
// background.js picks it up via storage.onChanged with no reload.
//
// Default-hidden: when nothing matches the current tab, the
// section stays out of the way so the popup is unchanged for
// most sites.
async function renderRoutingToggles(tab) {
  const section = document.getElementById("routing-section");
  const list = document.getElementById("routing-list");
  if (!section || !list || !tab || !tab.url) return;

  let host = "";
  try { host = new URL(tab.url).hostname; } catch { return; }

  const { downloadRules } = await chrome.storage.sync.get("downloadRules");
  const rules = Array.isArray(downloadRules) ? downloadRules : [];
  const matched = rules.filter((r) => routingHostMatches(host, r.hostPattern));
  if (matched.length === 0) {
    section.classList.add("hidden");
    return;
  }

  list.innerHTML = "";
  for (const rule of matched) {
    const row = document.createElement("label");
    row.className = "routing-row";
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = rule.enabled !== false;
    cb.addEventListener("change", async () => {
      rule.enabled = cb.checked;
      const { downloadRules } = await chrome.storage.sync.get("downloadRules");
      const all = Array.isArray(downloadRules) ? downloadRules : [];
      const idx = all.findIndex((r) => r.id === rule.id);
      if (idx >= 0) {
        all[idx] = { ...all[idx], enabled: cb.checked };
        await chrome.storage.sync.set({ downloadRules: all });
      }
      // Update the inline status line so the user gets feedback.
      hint.textContent = cb.checked
        ? `Auto-stashing to ${rule.subdir}/`
        : `Paused — downloads land in default ~/Downloads/`;
      hint.classList.toggle("muted", !cb.checked);
    });
    const text = document.createElement("span");
    text.className = "routing-name";
    text.textContent = rule.name || rule.hostPattern || "Unnamed rule";
    const hint = document.createElement("span");
    hint.className = "routing-hint";
    if (cb.checked) {
      hint.textContent = `Auto-stashing to ${rule.subdir}/`;
    } else {
      hint.textContent = `Paused — downloads land in default ~/Downloads/`;
      hint.classList.add("muted");
    }
    row.appendChild(cb);
    row.appendChild(text);
    row.appendChild(hint);
    list.appendChild(row);
  }
  section.classList.remove("hidden");
}

// Mirror of background.js's hostMatchesPattern. Kept local to
// avoid a module import — the popup runs in its own document
// context and bundling for one helper isn't worth it.
function routingHostMatches(host, pattern) {
  if (!host || !pattern) return false;
  const h = host.toLowerCase();
  const p = pattern.toLowerCase().trim();
  if (!p) return false;
  const suffix = p.startsWith("*.") ? p.slice(1) : p;
  if (suffix.startsWith(".")) return h === suffix.slice(1) || h.endsWith(suffix);
  return h === suffix || h.endsWith("." + suffix);
}

init();
