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

let currentTab = null;
let allTags = [];
let activeIndex = -1;
let searchTagActiveIndex = -1;
let existingItem = null;
let searchTimer = null;
let selectedText = "";
let tagJustAccepted = false;
let searchTagJustAccepted = false;

// --- Tabs ---

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    switchTab(tab.dataset.tab);
  });
});

function switchTab(target) {
  document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
  document.querySelector(`.tab[data-tab="${target}"]`).classList.add("active");

  stashView.classList.toggle("hidden", target !== "stash");
  searchView.classList.toggle("hidden", target !== "search");

  if (target === "search") {
    searchInput.focus();
    if (searchInput.value.trim().length === 0) {
      showTagCloud();
    }
  }
}

// --- Init ---

async function init() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  currentTab = tab;

  pageTitle.textContent = tab.title || "Untitled";
  pageUrl.textContent = tab.url || "";

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
  return tagsInput.value
    .split(",")
    .map((t) => t.trim().toLowerCase())
    .filter(Boolean);
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
      const idx = activeIndex >= 0 ? activeIndex : 0;
      selectTag(options[idx].querySelector(".tag-name").textContent);
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
      }
    : {
        action,
        url: currentTab.url,
        title: currentTab.title,
        tags,
        notes: notesInput.value.trim(),
        collection: collectionSelect.value,
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
  const matches = searchInput.value.matchAll(/tag:(\S+)/g);
  return Array.from(matches, (m) => m[1].toLowerCase());
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

  matches.slice(0, 8).forEach((tag) => {
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
    tagCloud.classList.add("hidden");
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

  // Check for tag: autocomplete first
  if (getSearchTagPartial() !== null) {
    updateSearchTagDropdown();
    return;
  }
  hideSearchTagDropdown();

  if (query.length === 0) {
    showTagCloud();
    return;
  }
  tagCloud.classList.add("hidden");
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
      e.preventDefault();
      const idx = searchTagActiveIndex >= 0 ? searchTagActiveIndex : 0;
      selectSearchTag(
        options[idx].querySelector(".tag-name").textContent
      );
      searchTagJustAccepted = true;
      return;
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
  showTagCloud();
  searchInput.focus();
});

function showTagCloud() {
  searchResults.innerHTML = "";
  tagCloud.classList.remove("hidden");
  if (tagCloud.children.length === 0) {
    loadTagCloud();
  }
}

async function loadRecent() {
  try {
    const resp = await sendMessage({ action: "search", query: "", limit: 20 });
    if (resp.ok) {
      renderResults(resp.items || [], "Recent items");
    }
  } catch {}
}

async function doSearch(query) {
  try {
    const resp = await sendMessage({ action: "search", query, limit: 20 });
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
      tagCloud.classList.add("hidden");
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

init();
