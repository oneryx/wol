"use strict";

const WAKE_WATCH_MS = 3 * 60 * 1000; // how long to watch for a woken device to come online
const POLL_MS = 15000;
const POLL_WAKING_MS = 3000;

const icons = {
  power: '<svg viewBox="0 0 24 24"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>',
  check: '<svg viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></svg>',
  edit: '<svg viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>',
  trash: '<svg viewBox="0 0 24 24"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>',
};

const state = {
  devices: [],
  online: {},  // id -> bool, only for devices with a host
  waking: {},  // id -> deadline (ms) while waiting for the device to come up
  sent: {},    // id -> true briefly after a packet was sent
  editing: null, // device being edited, null when adding
};

const $ = (sel) => document.querySelector(sel);
const grid = $("#devices");
const dialog = $("#device-dialog");
const form = $("#device-form");
const formError = $("#form-error");
const fields = form.elements; // not form.name: that is the form's own name attribute

// h builds an element; strings become text nodes so device data is never parsed as HTML.
function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") el.className = v;
    else if (k === "html") el.innerHTML = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  el.append(...children.filter((c) => c != null && c !== false));
  return el;
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

function toast(message, isError = false) {
  const el = h("div", { class: "toast" + (isError ? " error" : "") }, message);
  $("#toasts").append(el);
  setTimeout(() => el.remove(), isError ? 5000 : 3000);
}

// ---- Rendering ----

function statusOf(d) {
  if (!d.host) return null;
  const up = state.online[d.id];
  if (up) return ["online", "Online"];
  if (state.waking[d.id]) return ["waking", "Waking…"];
  if (up === undefined) return ["unknown", "Checking…"];
  return ["offline", "Offline"];
}

function renderCard(d) {
  const st = statusOf(d);
  const sent = state.sent[d.id];

  const meta = h("dl", { class: "meta" },
    h("dt", {}, "MAC"), h("dd", {}, d.mac),
    h("dt", {}, "Target"), h("dd", {}, `${d.broadcast}:${d.port}`),
    d.host && h("dt", {}, "Host"), d.host && h("dd", {}, d.host),
  );

  const wakeBtn = h("button", {
    class: "btn btn-primary wake-btn" + (sent ? " sent" : ""),
    type: "button",
    html: sent ? `${icons.check} Packet sent` : `${icons.power} Wake`,
    onclick: () => wake(d, wakeBtn),
  });

  return h("article", { class: "card" },
    h("div", { class: "card-head" },
      h("div", { class: "card-title" },
        h("h3", { title: d.name }, d.name),
        st && h("span", { class: `status ${st[0]}` }, st[1]),
      ),
      h("div", { class: "card-actions" },
        h("button", { class: "icon-btn", type: "button", title: "Edit", "aria-label": `Edit ${d.name}`, html: icons.edit, onclick: () => openDialog(d) }),
        h("button", { class: "icon-btn danger", type: "button", title: "Delete", "aria-label": `Delete ${d.name}`, html: icons.trash, onclick: () => remove(d) }),
      ),
    ),
    meta,
    wakeBtn,
  );
}

function render() {
  const { devices } = state;
  grid.replaceChildren(...devices.map(renderCard));
  $("#empty").hidden = devices.length > 0;

  const withHost = devices.filter((d) => d.host);
  const up = withHost.filter((d) => state.online[d.id]).length;
  let summary = `${devices.length} device${devices.length === 1 ? "" : "s"}`;
  if (withHost.length) summary += ` · ${up} online`;
  $("#summary").textContent = summary;
}

// ---- Actions ----

async function load() {
  try {
    state.devices = await api("GET", "/api/devices");
    state.devices.sort((a, b) => a.name.localeCompare(b.name));
    render();
  } catch (err) {
    $("#summary").textContent = "Failed to load devices";
    toast(err.message, true);
  }
}

async function wake(d, btn) {
  btn.disabled = true;
  try {
    await api("POST", `/api/devices/${encodeURIComponent(d.id)}/wake`);
    toast(`Magic packet sent to ${d.name}`);
    state.sent[d.id] = true;
    setTimeout(() => { delete state.sent[d.id]; render(); }, 2500);
    if (d.host && !state.online[d.id]) {
      state.waking[d.id] = Date.now() + WAKE_WATCH_MS;
      schedulePoll(POLL_WAKING_MS);
    }
  } catch (err) {
    toast(`Failed to wake ${d.name}: ${err.message}`, true);
  }
  render();
}

async function remove(d) {
  const confirmDialog = $("#confirm-dialog");
  $("#confirm-text").textContent = `“${d.name}” (${d.mac}) will be removed.`;
  confirmDialog.returnValue = "";
  confirmDialog.showModal();
  await new Promise((resolve) => confirmDialog.addEventListener("close", resolve, { once: true }));
  if (confirmDialog.returnValue !== "ok") return;

  try {
    await api("DELETE", `/api/devices/${encodeURIComponent(d.id)}`);
    state.devices = state.devices.filter((x) => x.id !== d.id);
    delete state.online[d.id];
    delete state.waking[d.id];
    render();
    toast(`Deleted ${d.name}`);
  } catch (err) {
    toast(err.message, true);
  }
}

function openDialog(device = null) {
  state.editing = device;
  $("#dialog-title").textContent = device ? `Edit ${device.name}` : "Add device";
  form.reset();
  formError.hidden = true;
  for (const input of form.querySelectorAll("input")) input.removeAttribute("aria-invalid");
  if (device) {
    fields.name.value = device.name;
    fields.mac.value = device.mac;
    fields.broadcast.value = device.broadcast;
    fields.port.value = device.port;
    fields.host.value = device.host || "";
  }
  dialog.showModal();
  fields.name.focus();
}

// Mirrors the server's MAC parsing so obvious typos are caught before submit.
function validMAC(s) {
  s = s.trim();
  return /^[0-9a-f]{12}$/i.test(s) ||
    /^([0-9a-f]{2}[:-]){5}[0-9a-f]{2}$/i.test(s) ||
    /^([0-9a-f]{4}\.){2}[0-9a-f]{4}$/i.test(s);
}

function showFormError(message, field) {
  formError.textContent = message;
  formError.hidden = false;
  if (field) {
    field.setAttribute("aria-invalid", "true");
    field.focus();
  }
}

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  for (const input of form.querySelectorAll("input")) input.removeAttribute("aria-invalid");
  formError.hidden = true;

  const body = {
    name: fields.name.value.trim(),
    mac: fields.mac.value.trim(),
    broadcast: fields.broadcast.value.trim(),
    port: fields.port.value ? Number(fields.port.value) : 0,
    host: fields.host.value.trim(),
  };
  if (!body.name) return showFormError("Name is required.", fields.name);
  if (!validMAC(body.mac)) return showFormError("Enter a MAC like 52:54:00:12:34:56.", fields.mac);

  const saveBtn = $("#save-btn");
  saveBtn.disabled = true;
  try {
    const editing = state.editing;
    const saved = editing
      ? await api("PUT", `/api/devices/${encodeURIComponent(editing.id)}`, body)
      : await api("POST", "/api/devices", body);
    if (editing) {
      state.devices = state.devices.map((x) => (x.id === saved.id ? saved : x));
      if (editing.host !== saved.host) delete state.online[saved.id];
    } else {
      state.devices.push(saved);
    }
    state.devices.sort((a, b) => a.name.localeCompare(b.name));
    dialog.close();
    render();
    toast(editing ? `Saved ${saved.name}` : `Added ${saved.name}`);
    if (saved.host) schedulePoll(0);
  } catch (err) {
    showFormError(err.message);
  } finally {
    saveBtn.disabled = false;
  }
});

// ---- Status polling ----

let pollTimer = null;

function schedulePoll(delay) {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(poll, delay);
}

async function poll() {
  const anyHost = state.devices.some((d) => d.host);
  if (anyHost && !document.hidden) {
    try {
      const online = await api("GET", "/api/status");
      const now = Date.now();
      for (const d of state.devices) {
        const deadline = state.waking[d.id];
        if (!deadline) continue;
        if (online[d.id]) {
          delete state.waking[d.id];
          toast(`${d.name} is online`);
        } else if (now > deadline) {
          delete state.waking[d.id];
          toast(`${d.name} hasn't come online yet`, true);
        }
      }
      state.online = online;
      render();
    } catch {
      // Transient; try again on the next tick.
    }
  }
  schedulePoll(Object.keys(state.waking).length ? POLL_WAKING_MS : POLL_MS);
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) schedulePoll(0);
});

// ---- Wiring ----

$("#add-btn").addEventListener("click", () => openDialog());
document.addEventListener("click", (e) => {
  const action = e.target.closest("[data-action]")?.dataset.action;
  if (action === "add") openDialog();
  if (action === "close") dialog.close();
});

load().then(() => schedulePoll(0));
