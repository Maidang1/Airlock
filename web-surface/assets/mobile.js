const statusEl = document.getElementById("status");
const listEl = document.getElementById("list");
const refreshBtn = document.getElementById("refresh");

async function loadSessions() {
  statusEl.textContent = "正在读取会话…";
  try {
    const res = await fetch("/api/mobile/cli/catalog");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    const sessions = data.sessions || [];
    statusEl.textContent = sessions.length ? `共 ${sessions.length} 个会话` : "暂无会话";
    listEl.replaceChildren(...sessions.map((s) => {
      const card = document.createElement("div");
      card.className = "session";
      const title = document.createElement("div");
      title.textContent = s.title || s.id;
      const meta = document.createElement("div");
      meta.className = "muted";
      meta.textContent = `${s.workspace || ""} · ${s.status || "ready"}`;
      card.append(title, meta);
      return card;
    }));
  } catch (err) {
    statusEl.textContent = `无法读取会话：${err.message || err}`;
  }
}

refreshBtn?.addEventListener("click", () => { void loadSessions(); });
void loadSessions();
