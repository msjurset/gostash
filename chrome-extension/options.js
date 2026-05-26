// Options page for the download router. Edits the same
// chrome.storage.sync entry that background.js reads, so changes take
// effect on the next download without an extension reload.

const KEY = "downloadRules";

const tbody = document.getElementById("rules-body");
const addBtn = document.getElementById("add-rule");
const statusEl = document.getElementById("status");

let rules = [];
let saveTimer = null;

function makeId() {
  return "rule-" + Date.now() + "-" + Math.random().toString(36).slice(2, 8);
}

function renderRow(rule) {
  const tr = document.createElement("tr");
  tr.dataset.id = rule.id;

  const onCell = document.createElement("td");
  const onInput = document.createElement("input");
  onInput.type = "checkbox";
  onInput.checked = !!rule.enabled;
  onInput.addEventListener("change", () => {
    rule.enabled = onInput.checked;
    scheduleSave();
  });
  onCell.appendChild(onInput);

  const nameCell = document.createElement("td");
  nameCell.appendChild(
    makeText(rule.name || "", (v) => {
      rule.name = v;
      scheduleSave();
    }, "Friendly name"),
  );

  const hostCell = document.createElement("td");
  hostCell.appendChild(
    makeText(rule.hostPattern || "", (v) => {
      rule.hostPattern = v;
      scheduleSave();
    }, "e.g. photos.google.com"),
  );

  const subdirCell = document.createElement("td");
  subdirCell.appendChild(
    makeText(rule.subdir || "", (v) => {
      rule.subdir = v;
      scheduleSave();
    }, "e.g. stash-google-photos"),
  );

  const notifyCell = document.createElement("td");
  const notifyInput = document.createElement("input");
  notifyInput.type = "checkbox";
  // Backwards-compat: the field was previously named `confirm` and
  // controlled a blocking popup. We replaced the popup with a passive
  // chrome.notifications toast; the boolean still defaults to true,
  // and a legacy `confirm` value migrates forward to `notify` on
  // first render.
  if (rule.notify === undefined && rule.confirm !== undefined) {
    rule.notify = rule.confirm;
    delete rule.confirm;
  }
  notifyInput.checked = rule.notify !== false;
  notifyInput.addEventListener("change", () => {
    rule.notify = notifyInput.checked;
    scheduleSave();
  });
  notifyCell.appendChild(notifyInput);

  const delCell = document.createElement("td");
  const delBtn = document.createElement("button");
  delBtn.className = "delete-btn";
  delBtn.textContent = "Delete";
  delBtn.addEventListener("click", () => {
    rules = rules.filter((r) => r.id !== rule.id);
    tr.remove();
    scheduleSave();
  });
  delCell.appendChild(delBtn);

  tr.appendChild(onCell);
  tr.appendChild(nameCell);
  tr.appendChild(hostCell);
  tr.appendChild(subdirCell);
  tr.appendChild(notifyCell);
  tr.appendChild(delCell);
  return tr;
}

function makeText(value, onChange, placeholder) {
  const input = document.createElement("input");
  input.type = "text";
  input.value = value;
  input.placeholder = placeholder || "";
  input.addEventListener("input", () => onChange(input.value));
  return input;
}

function scheduleSave() {
  statusEl.textContent = "Saving…";
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(save, 250);
}

async function save() {
  // Drop entries that have no host pattern AND no subdir — empties
  // would never match anything and the user almost certainly meant
  // to delete the row.
  const trimmed = rules.filter((r) =>
    (r.hostPattern && r.hostPattern.trim()) || (r.subdir && r.subdir.trim())
  );
  await chrome.storage.sync.set({ [KEY]: trimmed });
  statusEl.textContent = "Saved.";
  setTimeout(() => { statusEl.textContent = ""; }, 1500);
}

addBtn.addEventListener("click", () => {
  const rule = {
    id: makeId(),
    name: "",
    enabled: true,
    notify: true,
    hostPattern: "",
    subdir: "",
  };
  rules.push(rule);
  tbody.appendChild(renderRow(rule));
});

async function load() {
  const { [KEY]: stored } = await chrome.storage.sync.get(KEY);
  rules = Array.isArray(stored) ? stored : [];
  // Backfill IDs for legacy entries / hand-edited values.
  for (const r of rules) {
    if (!r.id) r.id = makeId();
    // Field rename: `confirm` (blocking popup) → `notify` (passive
    // toast). Migrate legacy entries forward so saved-on-disk rule
    // sets from an earlier extension version don't need a manual
    // edit.
    if (r.notify === undefined) {
      r.notify = r.confirm !== undefined ? r.confirm : true;
      delete r.confirm;
    }
    if (r.enabled === undefined) r.enabled = true;
  }
  tbody.innerHTML = "";
  for (const r of rules) tbody.appendChild(renderRow(r));
}

load();
