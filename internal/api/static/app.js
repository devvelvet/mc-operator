// mc-operator dashboard — vanilla JS client for the /api/v1 surface.

const state = {
  servers: [],
  events: [],
  history: [],
  selected: null,
  maxEvents: 100,
};

const $ = (sel) => document.querySelector(sel);

async function fetchServers() {
  try {
    const r = await fetch("/api/v1/servers");
    if (!r.ok) throw new Error("HTTP " + r.status);
    const body = await r.json();
    state.servers = body.servers || [];
    renderServers();
    if (state.selected) {
      renderDetail(state.selected);
    }
  } catch (e) {
    console.error("fetchServers failed", e);
  }
}

async function fetchHistory() {
  try {
    const r = await fetch("/api/v1/history");
    if (!r.ok) return;
    state.history = await r.json();
    renderHistory();
  } catch (e) {
    /* ignore */
  }
}

function renderServers() {
  const grid = $("#server-grid");
  if (state.servers.length === 0) {
    grid.innerHTML = '<div class="empty">No servers registered. Create a manifest to get started.</div>';
    return;
  }
  grid.innerHTML = state.servers.map(cardHTML).join("");
  grid.querySelectorAll(".card").forEach((el) => {
    el.addEventListener("click", (ev) => {
      if (ev.target.closest(".card-sync-btn")) return;
      openDetail(el.dataset.name);
    });
  });
  grid.querySelectorAll(".card-sync-btn").forEach((btn) => {
    btn.addEventListener("click", async (ev) => {
      ev.stopPropagation();
      const name = btn.dataset.name;
      btn.disabled = true;
      btn.textContent = "syncing...";
      try {
        const r = await fetch(`/api/v1/servers/${encodeURIComponent(name)}/sync`, { method: "POST" });
        if (!r.ok) {
          const body = await r.json().catch(() => ({}));
          alert(`sync failed: ${body.error || r.status}`);
        }
      } finally {
        btn.disabled = false;
        btn.textContent = "sync";
        fetchServers();
      }
    });
  });
}

function cardHTML(s) {
  const sync = s.sync || "Unknown";
  const health = s.health || "Unknown";
  const image = s.currentImage || "(no image)";
  const commit = s.lastCommit ? s.lastCommit.substring(0, 7) : "-";
  const port = s.port ? `:${s.port}` : "";
  return `
    <div class="card" data-name="${escapeHtml(s.name)}">
      <div class="card-head">
        <div class="card-name">${escapeHtml(s.name)}</div>
        <div class="card-port">${escapeHtml(port)}</div>
      </div>
      <div class="badges">
        <span class="badge sync-${sync}">${sync}</span>
        <span class="badge health-${health}">${health}</span>
      </div>
      <div class="card-meta">
        <div>image: ${escapeHtml(image)}</div>
        <div>commit: ${escapeHtml(commit)}</div>
      </div>
      <div class="card-actions">
        <button class="btn btn-small card-sync-btn" data-name="${escapeHtml(s.name)}">sync</button>
      </div>
    </div>
  `;
}

async function openDetail(name) {
  state.selected = name;
  try {
    const [detailRes, historyRes] = await Promise.all([
      fetch(`/api/v1/servers/${encodeURIComponent(name)}`),
      fetch(`/api/v1/servers/${encodeURIComponent(name)}/history`),
    ]);
    const detail = await detailRes.json();
    const history = await historyRes.json();
    renderDetail(name, detail, history);
    $("#detail-drawer").classList.add("open");
  } catch (e) {
    console.error(e);
  }
}

function closeDetail() {
  state.selected = null;
  $("#detail-drawer").classList.remove("open");
}

function renderDetail(name, detail, history) {
  if (!detail) return;
  const st = detail.state || {};
  const spec = detail.spec || {};
  const histHTML = (history || []).length === 0
    ? '<div class="empty small">no history yet</div>'
    : history.map((h) => `
        <div class="history-row">
          <span class="history-time">${new Date(h.startedAt).toLocaleString()}</span>
          <span class="history-kind">${escapeHtml(h.kind || "")}</span>
          <span class="history-status status-${escapeHtml(h.status || "")}">${escapeHtml(h.status || "")}</span>
          <span class="history-msg">${escapeHtml(h.message || "")}</span>
        </div>
      `).join("");

  $("#detail-body").innerHTML = `
    <div class="detail-header">
      <h3>${escapeHtml(name)}</h3>
      <button class="btn" id="detail-close">close</button>
    </div>
    <section class="detail-section">
      <h4>state</h4>
      <div class="kv"><span>sync</span><span class="badge sync-${st.sync || "Unknown"}">${st.sync || "Unknown"}</span></div>
      <div class="kv"><span>health</span><span class="badge health-${st.health || "Unknown"}">${st.health || "Unknown"}</span></div>
      <div class="kv"><span>current image</span><span class="mono">${escapeHtml(st.currentImage || "-")}</span></div>
      <div class="kv"><span>previous image</span><span class="mono">${escapeHtml(st.previousImage || "-")}</span></div>
      <div class="kv"><span>last commit</span><span class="mono">${escapeHtml(st.lastCommit || "-")}</span></div>
      <div class="kv"><span>port</span><span class="mono">${st.port || "-"}</span></div>
    </section>
    <section class="detail-section">
      <h4>spec</h4>
      <div class="kv"><span>type</span><span>${escapeHtml(spec.type || "-")}</span></div>
      <div class="kv"><span>version</span><span>${escapeHtml(spec.version || "-")}</span></div>
      <div class="kv"><span>memory</span><span>${(spec.resource && spec.resource.memoryMB) || "-"} MB</span></div>
      <div class="kv"><span>plugins</span><span>${(spec.plugins || []).length}</span></div>
    </section>
    <section class="detail-section">
      <h4>history</h4>
      <div class="history-list">${histHTML}</div>
    </section>
  `;
  $("#detail-close").addEventListener("click", closeDetail);
}

function escapeHtml(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function connectSSE() {
  const pill = $("#connection-status");
  const es = new EventSource("/api/v1/events");

  es.onopen = () => {
    pill.textContent = "live";
    pill.classList.add("live");
    pill.classList.remove("error");
  };
  es.onerror = () => {
    pill.textContent = "disconnected";
    pill.classList.remove("live");
    pill.classList.add("error");
  };

  const onAny = (ev) => {
    try {
      const data = JSON.parse(ev.data);
      pushEvent(data);
      if (["reconcile", "deploy", "rollback", "sync"].includes(data.type)) {
        fetchServers();
        fetchHistory();
      }
    } catch (e) {
      /* ignore */
    }
  };

  ["reconcile", "deploy", "rollback", "sync", "info", "error"].forEach((t) =>
    es.addEventListener(t, onAny)
  );
}

function pushEvent(ev) {
  state.events.unshift(ev);
  if (state.events.length > state.maxEvents) {
    state.events.length = state.maxEvents;
  }
  renderEvents();
}

function renderEvents() {
  const log = $("#event-log");
  log.innerHTML = state.events
    .map((e) => {
      const ts = new Date(e.timestamp).toLocaleTimeString();
      const msg = e.server ? `[${e.server}] ${e.message || ""}` : e.message || "";
      return `
        <li>
          <span class="event-time">${ts}</span>
          <span class="event-type">${escapeHtml(e.type || "info")}</span>
          <span class="event-message">${escapeHtml(msg)}</span>
        </li>
      `;
    })
    .join("");
}

function renderHistory() {
  // Reserved for a future "all history" panel; currently rendered only inside the detail drawer.
}

document.addEventListener("DOMContentLoaded", () => {
  $("#refresh-btn").addEventListener("click", () => {
    fetchServers();
    fetchHistory();
  });
  $("#clear-events").addEventListener("click", () => {
    state.events = [];
    renderEvents();
  });
  fetchServers();
  fetchHistory();
  connectSSE();
  setInterval(fetchServers, 30000);
});
