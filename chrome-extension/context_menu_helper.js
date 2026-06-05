/**
 * Content script to help the background service worker provide
 * dynamic context menus.
 */

// Listen for right-clicks to detect the target URL and send it
// to the background script before the menu opens.
window.addEventListener("contextmenu", (event) => {
  // Find the closest link or video URL
  let targetURL = "";
  
  // 1. Check for a link
  const link = event.target.closest("a[href]");
  if (link) {
    targetURL = link.href;
  } 
  // 2. Check for a video element
  else if (event.target.tagName === "VIDEO") {
    targetURL = event.target.src || window.location.href;
  }
  // 3. Fallback to page URL
  else {
    targetURL = window.location.href;
  }

  if (targetURL) {
    chrome.runtime.sendMessage({
      type: "context_menu_target",
      url: targetURL
    });
  }
}, true);
