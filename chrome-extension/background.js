const HOST_NAME = "com.gostash.host";

// --- Native Messaging ---

function sendNativeMessage(message) {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendNativeMessage(HOST_NAME, message, (response) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
      } else if (response && response.error) {
        reject(new Error(response.error));
      } else {
        resolve(response);
      }
    });
  });
}

// --- Context Menus ---

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({
    id: "stash-page",
    title: "Stash This Page",
    contexts: ["page"],
  });

  chrome.contextMenus.create({
    id: "stash-link",
    title: "Stash This Link",
    contexts: ["link"],
  });

  chrome.contextMenus.create({
    id: "stash-selection",
    title: "Stash Selected Text",
    contexts: ["selection"],
  });

  // Right-click on an <img> → fetch the image bytes and stash it
  // as a file (image-typed) item. Different from "Stash This Link"
  // because the IMAGE shows up in the stash, not just a URL.
  chrome.contextMenus.create({
    id: "stash-image",
    title: "Stash This Image",
    contexts: ["image"],
  });

  // Right-click on a non-link, non-image part of the page → opens
  // the file picker so the user can grab embedded images and
  // linked files from this page in one go.
  chrome.contextMenus.create({
    id: "stash-files-from-page",
    title: "Stash Files from Page…",
    contexts: ["page"],
  });
});

chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  try {
    let response;
    switch (info.menuItemId) {
      case "stash-page":
        response = await sendNativeMessage({
          action: "stash_url",
          url: tab.url,
          title: tab.title,
        });
        break;
      case "stash-link":
        response = await sendNativeMessage({
          action: "stash_url",
          url: info.linkUrl,
        });
        break;
      case "stash-selection":
        // Open the snippet picker so the user can choose between
        // "stash as a new snippet" and "append to an existing
        // item." The previous behavior — immediate stash_text —
        // is now the default within the picker (New mode) so a
        // one-keystroke flow is still possible.
        //
        // info.selectionText is plain text only: paragraphs collapse
        // to spaces, bullets/headings disappear. We pull the actual
        // selection range's HTML via a content-script injection and
        // convert it to Markdown so the stashed snippet preserves
        // structure (lists, paragraphs, bold/italic, links).
        const markdown = await getSelectionAsMarkdown(tab.id) || info.selectionText;
        await openSnippetDialog(tab, markdown);
        return;
      case "stash-image":
        // Fetch the image bytes from the EXTENSION context — the
        // service worker has host permissions for any URL and can
        // attach the user's session cookies, which is the only way
        // auth-gated CDN URLs (Gemini chat attachments, signed
        // GDrive previews, etc.) return a 200 instead of 403. We
        // ship the bytes through native messaging via stash_blob
        // rather than asking the native host to do its own HTTP
        // fetch (which has no cookie context).
        //
        // Title: try to read the <img>'s alt text via a content-
        // script injection. Falls back to the page title which is
        // still better than the URL token.
        response = await fetchAndStashBlob(info.srcUrl, {
          referer: tab.url,
          linkSource: tab.url,
          title: await readImageAlt(tab.id, info.srcUrl) || tab.title,
        });
        break;
      case "stash-files-from-page":
        // Open the picker UI in a popup window. background.js does
        // the actual fetch_url_list / pick calls; the picker.html
        // is just the user-facing checklist.
        await openPickerForPage(tab);
        return;
    }
    if (response && response.ok) {
      showBadge(tab.id, "\u2713", "#4CAF50");
    }
  } catch (err) {
    showBadge(tab.id, "!", "#F44336");
    console.error("Stash context menu error:", err);
  }
});

// --- Picker (Stash Files from Page) ---

// Page \u2192 picker payload: stash candidates in extension storage so
// picker.html can pull them on load. Using session-storage keyed by
// a one-shot token avoids race conditions when multiple pickers are
// open simultaneously (rare, but cheap to handle correctly).
async function openPickerForPage(tab) {
  const token = "picker-" + Date.now() + "-" + Math.random().toString(36).slice(2, 8);
  await chrome.storage.session.set({
    [token]: { state: "loading", pageURL: tab.url, pageTitle: tab.title },
  });
  await chrome.windows.create({
    url: chrome.runtime.getURL("picker.html") + "?token=" + token,
    type: "popup",
    width: 720,
    height: 600,
  });

  // Live-DOM scrape via executeScript runs in the user's actual tab
  // \u2014 handles SPA-rendered pages (Gemini, Notion, Twitter, etc.)
  // that the native host's plain HTTP scrape can't see, since their
  // initial HTML is just a shell and the content arrives after JS
  // hydration. Also: cookies / session state are already attached
  // to the page, so even auth-gated CDN URLs at least show up in
  // the candidate list (downloads of those still go through the
  // native host and may fail; that's a separate problem).
  let scrape;
  try {
    scrape = await scrapePageDOM(tab);
  } catch (err) {
    // Tabs the extension can't inject into (chrome://, file://, the
    // Web Store, etc.) fall back to the native host's HTTP scrape.
    try {
      const resp = await sendNativeMessage({ action: "fetch_url_list", url: tab.url });
      if (resp && resp.ok) {
        scrape = {
          page_url: resp.page_url || tab.url,
          page_title: resp.page_title || tab.title,
          candidates: resp.candidates || [],
        };
      } else {
        scrape = null;
      }
    } catch {}
  }

  if (scrape) {
    await chrome.storage.session.set({
      [token]: {
        state: "ready",
        pageURL: scrape.page_url || tab.url,
        pageTitle: scrape.page_title || tab.title,
        candidates: scrape.candidates || [],
      },
    });
  } else {
    await chrome.storage.session.set({
      [token]: { state: "error", error: "Couldn't scan the page (script injection failed)." },
    });
  }
}

// getSelectionAsMarkdown reads the current selection in the given
// tab as HTML, runs an in-page HTML→Markdown converter, and returns
// the Markdown string. Returns "" if no selection or injection
// fails — caller should fall back to info.selectionText.
async function getSelectionAsMarkdown(tabId) {
  try {
    const [r] = await chrome.scripting.executeScript({
      target: { tabId },
      func: selectionToMarkdownInPage,
    });
    return (r && typeof r.result === "string") ? r.result : "";
  } catch {
    return "";
  }
}

// Runs in the page context. Walks the selected Range's cloned
// contents and emits Markdown, preserving paragraph breaks, list
// structure, headings, basic inline formatting, links, and code.
// Deliberately small — a fuller library (Turndown, etc.) would be
// nicer but bundling it would balloon the extension. For the
// "stash selected text" workflow, getting bullets and paragraphs
// right covers ~95% of the value.
function selectionToMarkdownInPage() {
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return "";
  const range = sel.getRangeAt(0);
  const frag = range.cloneContents();
  // Wrap in a host element so we can walk children uniformly.
  const host = document.createElement("div");
  host.appendChild(frag);

  // Resolve absolute URLs for relative hrefs in the selection.
  const baseURL = location.href;
  function absURL(href) {
    try { return new URL(href, baseURL).href; } catch { return href; }
  }

  // Walk the DOM, building Markdown line-by-line. Block elements
  // emit double-newlines; inline elements emit their text with
  // surrounding markup. List context tracks indentation for nested
  // bullets.
  function walk(node, ctx) {
    if (node.nodeType === Node.TEXT_NODE) {
      // Collapse internal whitespace runs but keep meaningful
      // boundaries. The browser's renderer already collapsed
      // whitespace per CSS, so this mostly avoids stray double
      // spaces inside paragraphs.
      let t = node.nodeValue.replace(/[ \t\n\r\f]+/g, " ");
      // Escape Markdown specials that would otherwise be parsed
      // (backslash, *, _, backticks, brackets, hashes at line
      // starts, etc.). Keep simple — over-escaping is uglier
      // than under-escaping for casual reading.
      t = t.replace(/[\\*_`]/g, "\\$&");
      return t;
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return "";

    const tag = node.tagName.toLowerCase();

    // Block elements — wrap in newlines so paragraphs survive.
    switch (tag) {
      case "p":
      case "div":
      case "section":
      case "article":
        return "\n\n" + walkChildren(node, ctx) + "\n\n";
      case "br":
        return "  \n";
      case "h1": return "\n\n# "      + walkChildren(node, ctx) + "\n\n";
      case "h2": return "\n\n## "     + walkChildren(node, ctx) + "\n\n";
      case "h3": return "\n\n### "    + walkChildren(node, ctx) + "\n\n";
      case "h4": return "\n\n#### "   + walkChildren(node, ctx) + "\n\n";
      case "h5": return "\n\n##### "  + walkChildren(node, ctx) + "\n\n";
      case "h6": return "\n\n###### " + walkChildren(node, ctx) + "\n\n";
      case "blockquote": {
        const inner = walkChildren(node, ctx).trim();
        return "\n\n" + inner.split("\n").map((l) => "> " + l).join("\n") + "\n\n";
      }
      case "hr":
        return "\n\n---\n\n";
      case "pre": {
        // Fenced code block — strip nested <code> wrapping so we
        // don't double-up backticks.
        const text = node.textContent;
        return "\n\n```\n" + text.replace(/\n+$/, "") + "\n```\n\n";
      }
      case "ul":
      case "ol": {
        const items = [];
        const isOrdered = tag === "ol";
        let idx = 1;
        for (const child of node.children) {
          if (child.tagName.toLowerCase() !== "li") continue;
          const marker = isOrdered ? `${idx}. ` : "- ";
          idx++;
          // Indent nested list contents two spaces beyond the
          // parent marker.
          const childCtx = { ...ctx, indent: (ctx.indent || "") + "  " };
          const body = walkChildren(child, childCtx).trim();
          // Multi-line li body: continue lines get the indent
          // applied so the renderer treats them as one item.
          const lines = body.split("\n");
          const head = lines[0] || "";
          const tail = lines.slice(1).map((l) => l ? childCtx.indent + l : l).join("\n");
          items.push(ctx.indent + marker + head + (tail ? "\n" + tail : ""));
        }
        return "\n\n" + items.join("\n") + "\n\n";
      }
      case "li":
        // Bare <li> outside a list (shouldn't happen from a real
        // selection but defensive). Treat as bullet.
        return "- " + walkChildren(node, ctx).trim() + "\n";

      // Inline elements
      case "strong":
      case "b":
        return "**" + walkChildren(node, ctx) + "**";
      case "em":
      case "i":
        return "*" + walkChildren(node, ctx) + "*";
      case "code":
        return "`" + node.textContent + "`";
      case "del":
      case "s":
      case "strike":
        return "~~" + walkChildren(node, ctx) + "~~";
      case "a": {
        const href = node.getAttribute("href");
        const text = walkChildren(node, ctx).trim();
        if (!href) return text;
        return "[" + text + "](" + absURL(href) + ")";
      }
      case "img": {
        const alt = node.getAttribute("alt") || "";
        const src = node.getAttribute("src") || "";
        return "![" + alt + "](" + absURL(src) + ")";
      }
      case "script":
      case "style":
      case "noscript":
        return "";
      default:
        // Unknown / unhandled element — recurse so inner content
        // still surfaces.
        return walkChildren(node, ctx);
    }
  }

  function walkChildren(node, ctx) {
    let out = "";
    for (const child of node.childNodes) {
      out += walk(child, ctx);
    }
    return out;
  }

  let md = walkChildren(host, { indent: "" });
  // Normalize: collapse 3+ newlines down to 2; trim leading/trailing.
  md = md.replace(/\n{3,}/g, "\n\n").trim();
  return md;
}

// openSnippetDialog stages the selection + page context in
// chrome.storage.session, then opens snippet.html in a popup
// window. snippet.js reads the token from the URL and pulls the
// payload on load.
async function openSnippetDialog(tab, selection) {
  const token = "snippet-" + Date.now() + "-" + Math.random().toString(36).slice(2, 8);
  await chrome.storage.session.set({
    [token]: {
      pageURL: tab.url,
      pageTitle: tab.title,
      selection: selection || "",
    },
  });
  await chrome.windows.create({
    url: chrome.runtime.getURL("snippet.html") + "?token=" + token,
    type: "popup",
    width: 560,
    height: 600,
  });
}

// scrapePageDOM injects a function into the active tab and runs it
// against the rendered DOM. Returns the same {page_url, page_title,
// candidates} shape as the native host's fetch_url_list, so the
// picker doesn't have to care which path produced the data.
async function scrapePageDOM(tab) {
  const results = await chrome.scripting.executeScript({
    target: { tabId: tab.id },
    func: collectCandidatesInPage,
  });
  if (!results || !results[0]) {
    throw new Error("no script result");
  }
  return results[0].result;
}

// Runs in the page context (no closure \u2014 needs to be self-contained).
// Walks rendered <img> and <a> tags, dedupes by absolute URL, and
// returns image candidates plus file-extension-matched links.
//
// For images we prefer a higher-resolution source over the rendered
// thumbnail when one is recoverable, in this priority:
//   1. Parent <a href> that itself points at an image
//   2. data-fullsize / data-original / data-src attributes
//   3. The largest entry in the <img>'s srcset
//   4. The Google-CDN `=sN` / `=wN-hN` size suffix bumped to s0
//   5. img.currentSrc \u2192 img.src (the rendered preview)
function collectCandidatesInPage() {
  const stashable = new Set([
    ".pdf", ".doc", ".docx", ".rtf", ".ppt", ".pptx", ".xls", ".xlsx",
    ".csv", ".tsv", ".txt", ".md", ".epub", ".mobi",
    ".zip", ".tar", ".gz", ".tgz", ".rar", ".7z", ".bz2",
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic", ".svg", ".bmp", ".tiff",
    ".mp3", ".wav", ".flac", ".ogg", ".m4a",
    ".mp4", ".mov", ".avi", ".mkv", ".webm",
    ".json", ".xml", ".yaml", ".yml",
    ".iso", ".dmg",
  ]);
  const imageExtensions = new Set([
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic", ".svg", ".bmp", ".tiff",
  ]);

  const seen = new Set();
  const candidates = [];

  function basename(u) {
    try {
      const url = new URL(u);
      const last = url.pathname.split("/").pop() || "";
      return last || u;
    } catch {
      return u;
    }
  }

  function add(rawURL, label, kind) {
    if (!rawURL) return;
    let abs;
    try {
      abs = new URL(rawURL, location.href).href;
    } catch {
      return;
    }
    if (!abs.startsWith("http://") && !abs.startsWith("https://")) return;
    if (seen.has(abs)) return;
    seen.add(abs);
    candidates.push({ url: abs, label: label || basename(abs), kind });
  }

  // Pick the largest URL out of a srcset string. Returns "" when
  // srcset doesn't carry usable width descriptors.
  function largestSrcset(ss) {
    let bestURL = "";
    let bestW = -1;
    for (const part of ss.split(",")) {
      const fields = part.trim().split(/\s+/);
      if (!fields[0]) continue;
      const u = fields[0];
      let w = 0;
      if (fields[1] && fields[1].endsWith("w")) {
        w = parseInt(fields[1].slice(0, -1), 10) || 0;
      } else if (fields[1] && fields[1].endsWith("x")) {
        // density descriptor \u2014 multiply by a baseline so the
        // largest density still wins over a missing one.
        w = (parseFloat(fields[1].slice(0, -1)) || 0) * 1000;
      }
      if (!bestURL || w > bestW) {
        bestURL = u;
        bestW = w;
      }
    }
    return bestURL;
  }

  // Try to upgrade a Google CDN URL to its original size by
  // replacing or appending the trailing `=sN` / `=wN-hN-\u2026`
  // suffix with `=s0` (Google CDN convention for "no resize").
  // Returns the original URL unchanged when the pattern doesn't
  // match. Best-effort only; not all Google CDNs honor `=s0`.
  function upgradeGoogleCDN(rawURL) {
    let host;
    try { host = new URL(rawURL, location.href).hostname; } catch { return rawURL; }
    if (!host.endsWith("googleusercontent.com")) return rawURL;
    // The size suffix sits after the last `=` in the URL.
    const eq = rawURL.lastIndexOf("=");
    if (eq >= 0 && /^=[swh\-\d]+$/.test(rawURL.slice(eq))) {
      return rawURL.slice(0, eq) + "=s0";
    }
    // No suffix \u2014 append one. Some Gemini-style URLs don't honor
    // this, in which case the server returns the default-size
    // body and we're no worse off.
    return rawURL + "=s0";
  }

  function looksLikeImage(href) {
    let path;
    try {
      path = new URL(href, location.href).pathname.toLowerCase();
    } catch { return false; }
    const dot = path.lastIndexOf(".");
    if (dot < 0) return false;
    return imageExtensions.has(path.slice(dot));
  }

  // Images: walk every <img>, but prefer high-res sources when
  // they're available. The priority chain mirrors what a manual
  // "view full size" workflow would look at.
  const handledByParent = new Set();
  for (const img of document.querySelectorAll("img")) {
    // 1. Parent <a href> pointing at an image \u2014 classic
    //    thumbnail-links-to-original pattern.
    let chosen = "";
    const parent = img.closest("a[href]");
    if (parent && looksLikeImage(parent.href)) {
      chosen = parent.href;
      handledByParent.add(parent.href);
    }
    // 2. Common data-* attributes for "real" source.
    if (!chosen) {
      for (const attr of ["data-fullsize", "data-original", "data-src", "data-zoom-src", "data-hires"]) {
        const v = img.getAttribute(attr);
        if (v) { chosen = v; break; }
      }
    }
    // 3. srcset's largest entry.
    if (!chosen) {
      const ss = img.getAttribute("srcset");
      if (ss) chosen = largestSrcset(ss);
    }
    // 4. Google-CDN suffix bump.
    if (!chosen) {
      const cur = img.currentSrc || img.src;
      if (cur) chosen = upgradeGoogleCDN(cur);
    }
    // 5. Fallback to rendered src.
    if (!chosen) chosen = img.currentSrc || img.src;
    add(chosen, img.alt || "", "image");
  }

  // Links: file-extension-matched, but skip ones we already
  // promoted via the parent-<a> rule above (they were emitted as
  // image candidates, not as separate link candidates).
  for (const a of document.querySelectorAll("a[href]")) {
    if (handledByParent.has(a.href)) continue;
    let path = "";
    try {
      path = new URL(a.href, location.href).pathname.toLowerCase();
    } catch {
      continue;
    }
    let ext = "";
    const dot = path.lastIndexOf(".");
    if (dot >= 0) ext = path.slice(dot);
    if (!stashable.has(ext)) continue;
    add(a.href, (a.textContent || "").trim(), "link");
  }

  // Sort: images before links (mirrors the native host's behavior
  // and matches what users want most of the time).
  candidates.sort((a, b) => {
    if (a.kind === b.kind) return 0;
    return a.kind === "image" ? -1 : 1;
  });

  return {
    page_url: location.href,
    page_title: document.title,
    candidates,
  };
}

// --- Tab URL Check (Already Stashed Indicator) ---

// Icon variants. The "stashed" set has a green circle + white
// checkmark composited into the bottom-right corner. The PNGs are
// generated once by chrome-extension/icons/generate-stashed-overlay.swift
// and committed alongside the source icons.
//
// Using setIcon (rather than setBadgeText) gives pixel-level
// control over the indicator's size and placement. Chrome's badge
// slot scales text to fill the cell, which makes any non-trivial
// glyph eat most of the icon.
const ICON_DEFAULT = {
  16: "icons/icon16.png",
  48: "icons/icon48.png",
  128: "icons/icon128.png",
};
const ICON_STASHED = {
  16: "icons/icon16-stashed.png",
  48: "icons/icon48-stashed.png",
  128: "icons/icon128-stashed.png",
};

async function refreshStashedIcon(tabId, url) {
  // Reset to the default first so a tab whose URL has changed
  // doesn't show a stale "stashed" icon while the native call is
  // in flight.
  chrome.action.setIcon({ tabId, path: ICON_DEFAULT });
  if (!url) return;
  if (!url.startsWith("http://") && !url.startsWith("https://")) return;
  try {
    const response = await sendNativeMessage({
      action: "check_url",
      url,
    });
    if (response && response.ok && response.exists) {
      chrome.action.setIcon({ tabId, path: ICON_STASHED });
    }
  } catch {
    // Native host unavailable — leave the default icon in place.
  }
}

chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  // Fire on full page load AND on SPA route changes (the second
  // case shows up as `changeInfo.url` set even when status stays
  // at "complete" from the previous render).
  if (changeInfo.status === "complete" || changeInfo.url) {
    refreshStashedIcon(tabId, tab.url);
  }
});

// Tab switch — the user clicked over to a tab that might have
// loaded before the extension installed/reloaded, so its icon was
// never set. Re-run the check on activation.
chrome.tabs.onActivated.addListener(async ({ tabId }) => {
  try {
    const tab = await chrome.tabs.get(tabId);
    refreshStashedIcon(tabId, tab.url);
  } catch {
    // Tab vanished between the activation event and the get().
  }
});

// On extension startup / reload, sweep every existing tab so
// already-open tabs get their icons set without waiting for a
// navigation. Without this, the icon only flips after the user
// triggers `onUpdated` or `onActivated` for the first time.
chrome.runtime.onStartup.addListener(sweepAllTabs);
chrome.runtime.onInstalled.addListener(sweepAllTabs);
async function sweepAllTabs() {
  try {
    const tabs = await chrome.tabs.query({});
    for (const tab of tabs) {
      if (tab.id !== undefined) refreshStashedIcon(tab.id, tab.url);
    }
  } catch {
    // chrome.tabs not available yet — fine; onUpdated/onActivated
    // will catch up.
  }
}
// Also call once at top-level so a service-worker wake-up
// (not a fresh install / startup) catches up immediately.
sweepAllTabs();

// --- Omnibox ---

chrome.omnibox.onInputChanged.addListener(async (text, suggest) => {
  if (text.length < 2) return;

  // If the user is typing a tag: filter, suggest matching tag names
  const tagPartial = text.match(/(?:^|\s)tag:(\S*)$/);
  if (tagPartial) {
    const partial = tagPartial[1].toLowerCase();
    // Collect already-completed tags so we don't suggest them again
    const existing = Array.from(
      text.matchAll(/tag:(\S+)/g),
      (m) => m[1].toLowerCase()
    );
    try {
      const response = await sendNativeMessage({ action: "list_tags" });
      if (response && response.ok && response.tags) {
        const prefix = text.substring(
          0,
          text.length - tagPartial[0].length + (tagPartial[0][0] === " " ? 1 : 0)
        );
        const suggestions = response.tags
          .filter(
            (t) =>
              t.name.toLowerCase().includes(partial) &&
              !existing.includes(t.name.toLowerCase())
          )
          .slice(0, 6)
          .map((t) => ({
            content: `${prefix}tag:${t.name} `.trimStart(),
            description: `<match>tag:${escapeXml(t.name)}</match> <dim>(${t.count} items)</dim>`,
          }));
        suggest(suggestions);
      }
    } catch {}
    return;
  }

  try {
    const response = await sendNativeMessage({
      action: "search",
      query: text,
      limit: 6,
    });
    if (response && response.ok && response.items) {
      const suggestions = response.items.map((item) => ({
        content: item.url || item.title,
        description: `${escapeXml(item.title)} - <dim>${escapeXml(item.type)}</dim>`,
      }));
      suggest(suggestions);
    }
  } catch {
    // Silently fail
  }
});

chrome.omnibox.onInputEntered.addListener(async (text, disposition) => {
  // Direct URL from suggestion — open it
  if (text.startsWith("http://") || text.startsWith("https://")) {
    chrome.tabs.update({ url: text });
    return;
  }

  // Tag-based or text query — run search and open the first result
  try {
    const response = await sendNativeMessage({
      action: "search",
      query: text,
      limit: 1,
    });
    if (response && response.ok && response.items && response.items.length > 0) {
      const url = response.items[0].url;
      if (url) {
        chrome.tabs.update({ url });
      }
    }
  } catch {}
});

// --- Message Handler (from popup / picker / snippet) ---

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message.type === "native") {
    sendNativeMessage(message.payload)
      .then(sendResponse)
      .catch((err) => sendResponse({ ok: false, error: err.message }));
    return true; // async response
  }
  if (message.type === "fetch_and_stash") {
    // Picker calls this per-URL to bypass the native host's
    // cookie-less HTTP fetch.
    fetchAndStashBlob(message.url, {
      referer: message.referer,
      linkSource: message.link_source,
      tags: message.tags,
      collection: message.collection,
      title: message.title,
    })
      .then(sendResponse)
      .catch((err) => sendResponse({ ok: false, error: err.message }));
    return true;
  }
  if (message.type === "fetch_thumb") {
    // Picker calls this per-image when rendering thumbnails. Routed
    // through the service worker (rather than fetching from the
    // picker page directly) because MV3 extension pages still run
    // cross-origin `fetch` through the standard CORS preflight even
    // with `<all_urls>` host_permissions — and most image CDNs don't
    // send the headers that would allow it. The service worker's
    // fetch bypasses that check.
    fetchThumbDataURL(message.url)
      .then(sendResponse)
      .catch((err) => sendResponse({ ok: false, error: err.message }));
    return true;
  }
});

async function fetchThumbDataURL(url) {
  let res;
  try {
    res = await fetch(url, { credentials: "include" });
  } catch (err) {
    return { ok: false, error: "fetch: " + err.message };
  }
  if (!res.ok) {
    return { ok: false, error: "HTTP " + res.status };
  }
  const mime = res.headers.get("Content-Type") || "image/jpeg";
  if (!mime.startsWith("image/")) {
    return { ok: false, error: "not an image (" + mime + ")" };
  }
  const buf = await res.arrayBuffer();
  const base64 = arrayBufferToBase64(buf);
  return { ok: true, dataURL: "data:" + mime + ";base64," + base64 };
}

// fetchAndStashBlob does the auth-aware download path: fetch from
// the extension's service worker (which has host_permissions and
// will attach session cookies for any origin), base64-encode, send
// to the native host as a `stash_blob` action. The host writes the
// bytes to its filestore and returns the new item.
async function fetchAndStashBlob(url, opts = {}) {
  const headers = {};
  if (opts.referer) {
    // Some CDNs check Referer for hot-link protection. The browser
    // strips most Referer overrides for security, but it's harmless
    // to set when the call goes through to fetch().
    headers["Referer"] = opts.referer;
  }
  let res;
  try {
    res = await fetch(url, { credentials: "include", headers });
  } catch (err) {
    return { ok: false, error: "fetch: " + err.message };
  }
  if (!res.ok) {
    return { ok: false, error: "HTTP " + res.status };
  }
  const buf = await res.arrayBuffer();
  const base64 = arrayBufferToBase64(buf);
  const mime = res.headers.get("Content-Type") || "application/octet-stream";

  return sendNativeMessage({
    action: "stash_blob",
    url,
    blob_base64: base64,
    blob_mime: mime,
    link_source: opts.linkSource || "",
    tags: opts.tags || [],
    collection: opts.collection || "",
    title: opts.title || "",
  });
}

// readImageAlt looks up the <img> with the given src and returns
// its alt attribute. Used by "Stash This Image" so the stashed
// item carries a human-readable title instead of the URL basename
// (which is just a CDN token for auth-gated images).
async function readImageAlt(tabId, srcUrl) {
  try {
    const [r] = await chrome.scripting.executeScript({
      target: { tabId },
      args: [srcUrl],
      func: (src) => {
        for (const img of document.querySelectorAll("img")) {
          const cur = img.currentSrc || img.src;
          if (cur === src && img.alt) return img.alt.trim();
        }
        return "";
      },
    });
    return r?.result || "";
  } catch {
    return "";
  }
}

// arrayBufferToBase64 converts an ArrayBuffer to base64. Built-in
// btoa requires a binary string; we go through Uint8Array in
// chunks to avoid a "Maximum call stack size exceeded" on large
// blobs (>~100KB) when passing the whole array to String.fromCharCode.
function arrayBufferToBase64(buf) {
  const bytes = new Uint8Array(buf);
  const chunkSize = 0x8000; // 32k chars per call to fromCharCode
  let binary = "";
  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode.apply(
      null,
      bytes.subarray(i, i + chunkSize)
    );
  }
  return btoa(binary);
}

// --- Helpers ---

function showBadge(tabId, text, color) {
  chrome.action.setBadgeText({ tabId, text });
  chrome.action.setBadgeBackgroundColor({ tabId, color });
  setTimeout(() => {
    chrome.action.setBadgeText({ tabId, text: "" });
  }, 2000);
}

function escapeXml(str) {
  if (!str) return "";
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
