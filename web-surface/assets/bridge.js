let contextState = { connection_id: "local" };

export function mygoAvailable() {
  return typeof window !== "undefined" && typeof window.mygo?.call === "function";
}

export function callBridge(method, ...args) {
  if (!mygoAvailable()) {
    return Promise.reject(new Error("native bridge unavailable"));
  }
  return window.mygo.call("VNCBridge." + method, ...args);
}

export function getContext() {
  return contextState;
}

export function setContext(next = {}) {
  contextState = { ...contextState, ...next };
  return contextState;
}

function shouldSendConnectionHeader(path) {
  return (
    typeof path === "string" &&
    !path.startsWith("/client/") &&
    path !== "/session" &&
    path !== "/launch" &&
    path !== "/handoff"
  );
}

export async function establishSession(token) {
  if (!token) return;
  const resp = await fetch("/api/session", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
  });
  if (!resp.ok) {
    throw new Error((await resp.text()) || `HTTP ${resp.status}`);
  }
}

let sessionPromise = null;

export async function ensureSession() {
  if (!mygoAvailable()) return false;
  if (!sessionPromise) {
    sessionPromise = (async () => {
      const token = await callBridge("RefreshSessionTicket");
      if (!token) return false;
      await establishSession(token);
      return true;
    })().finally(() => {
      sessionPromise = null;
    });
  }
  return sessionPromise;
}

export async function api(path, body, options = {}) {
  const headers = { ...(options.headers || {}) };
  const connId = contextState.connection_id;
  if (connId && connId !== "local" && shouldSendConnectionHeader(path)) {
    headers["X-Cage-Connection"] = connId;
  }
  const init = {
    method: options.method || (body !== undefined ? "POST" : "GET"),
    credentials: "same-origin",
    headers,
  };
  if (body !== undefined && options.rawBody !== true) {
    if (!headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    init.body = typeof body === "string" ? body : JSON.stringify(body);
  } else if (body !== undefined) {
    init.body = body;
  }
  const resp = await fetch("/api" + path, init);
  if (resp.status === 401 && !options._retried && mygoAvailable()) {
    const refreshed = await ensureSession().catch(() => false);
    if (refreshed) {
      return api(path, body, { ...options, _retried: true });
    }
  }
  if (!resp.ok) {
    const text = (await resp.text()).trim();
    throw new Error(text || `HTTP ${resp.status}`);
  }
  const ct = resp.headers.get("content-type") || "";
  if (ct.includes("application/json")) {
    return resp.json();
  }
  return resp.text();
}

export function runtimeSocketURL(path) {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = new URL(`${proto}//${location.host}/api${path}`);
  if (contextState.connection_id && contextState.connection_id !== "local") {
    url.searchParams.set("connection", contextState.connection_id);
  }
  return url.toString();
}
