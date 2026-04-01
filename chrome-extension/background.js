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
        response = await sendNativeMessage({
          action: "stash_text",
          text: info.selectionText,
          title: tab.title + " (selection)",
        });
        break;
    }
    if (response && response.ok) {
      showBadge(tab.id, "\u2713", "#4CAF50");
    }
  } catch (err) {
    showBadge(tab.id, "!", "#F44336");
    console.error("Stash context menu error:", err);
  }
});

// --- Tab URL Check (Already Stashed Badge) ---

chrome.tabs.onUpdated.addListener(async (tabId, changeInfo, tab) => {
  if (changeInfo.status !== "complete" || !tab.url) return;
  if (!tab.url.startsWith("http://") && !tab.url.startsWith("https://")) return;

  try {
    const response = await sendNativeMessage({
      action: "check_url",
      url: tab.url,
    });
    if (response && response.ok && response.exists) {
      chrome.action.setBadgeText({ tabId, text: "\u2022" });
      chrome.action.setBadgeBackgroundColor({ tabId, color: "#2196F3" });
    } else {
      chrome.action.setBadgeText({ tabId, text: "" });
    }
  } catch {
    // Native host not available — clear badge silently
    chrome.action.setBadgeText({ tabId, text: "" });
  }
});

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

// --- Message Handler (from popup) ---

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message.type === "native") {
    sendNativeMessage(message.payload)
      .then(sendResponse)
      .catch((err) => sendResponse({ ok: false, error: err.message }));
    return true; // async response
  }
});

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
