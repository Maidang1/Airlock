import RFB from "/novnc/core/rfb.js";
import { api, callBridge, ensureSession, establishSession, mygoAvailable, runtimeSocketURL, setContext } from "./bridge.js";
import { displayGeometry, displaySizeSync, watchPixelRatio } from "./remote-display.js";
import { installLocalCursorFallback } from "./remote-cursor.js";
import { guestKeys } from "./guest-keys.js";
import { prepareClipboardPaste, sendImage } from "./image-transfer.js";
import { sendFiles } from "./file-transfer.js";
import { installRemoteInput } from "./remote-input.js";

const stage = document.getElementById("stage");
const params = new URLSearchParams(location.search);

const state = {
  mode: params.get("mode") === "desktop" ? "desktop" : "browser",
  tabId: params.get("tab") || "",
  connectionId: params.get("connection_id") || "local",
  keyboard: params.get("keyboard") || "mac",
  guestPlatform: params.get("guestPlatform") || "linux",
  systemClipboard: params.get("systemClipboard") !== "false",
  sharedClipboard: params.get("sharedClipboard") === "true",
  zoom: Number(params.get("zoom") || "1") || 1,
  active: true,
};
setContext({ connection_id: state.connectionId });

let client = null;
let input = null;
let sizeSync = null;
let resizeObserver = null;
let unwatchDensity = null;
let keys = null;
let reconnectTimer = null;
let failures = 0;
let disposed = false;

const sessionReady = (async () => {
  const hash = location.hash ? decodeURIComponent(location.hash.slice(1)) : "";
  if (hash) {
    history.replaceState(null, "", location.pathname + location.search);
    try {
      await establishSession(hash);
      return;
    } catch {}
  }
  if (mygoAvailable()) {
    try { await ensureSession(); } catch {}
  }
})();

const reportStatus = (text) => {
  if (mygoAvailable()) void callBridge("ReportStatus", state.mode, String(text || "")).catch(() => {});
};
const reportConnection = (status, sizeError = false) => {
  if (mygoAvailable()) void callBridge("ReportConnection", state.mode, status, Boolean(sizeError)).catch(() => {});
};
const reportError = (text) => {
  if (mygoAvailable()) void callBridge("ReportError", state.mode, String(text || "")).catch(() => {});
};
const reportFallback = (text, hint) => {
  if (mygoAvailable()) void callBridge("ReportFallback", state.mode, String(text || ""), String(hint || "")).catch(() => {});
};

function cleanup() {
  clearTimeout(reconnectTimer);
  keys?.stop();
  keys = null;
  input?.dispose();
  input = null;
  sizeSync?.dispose();
  sizeSync = null;
  resizeObserver?.disconnect();
  resizeObserver = null;
  unwatchDensity?.();
  unwatchDensity = null;
  if (client) {
    try { client.disconnect(); } catch {}
    client = null;
  }
}

async function connect() {
  if (disposed) return;
  cleanup();
  reportConnection("connecting", false);
  await sessionReady;
  if (disposed) return;
  const desktop = state.mode === "desktop";
  const prefix = desktop ? "/claude-desktop" : "";
  try {
    const creds = await api(`${prefix}/vnc-credentials`);
    if (disposed) return;
    let followScreen = creds.dynamic_scale === true;
    const density = creds.display_scale;
    const rfb = new RFB(stage, runtimeSocketURL(`${prefix}/vnc`), {
      credentials: { password: creds.password },
      cageBrowserDisplay: !desktop,
      cageCursorFallback: desktop && state.guestPlatform === "windows",
      cageWheelSink: !desktop && creds.pixel_wheel === true
        ? (wheel) => api("/browser/wheel", wheel).then((reply) => reply?.handled === true)
        : undefined,
    });
    client = rfb;
    if (!/^(windows|darwin|macos)/.test(state.guestPlatform || "")) {
      keys = guestKeys(rfb.sendKey.bind(rfb));
      rfb.sendKey = keys.sendKey;
    }
    installLocalCursorFallback(rfb._cursor, !desktop && ["darwin", "macos"].includes(state.guestPlatform));
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
    rfb.background = "#ffffff";
    rfb.qualityLevel = 8;
    rfb.compressionLevel = 2;

    const retry = () => {
      if (disposed || client !== rfb) return;
      reportConnection("disconnected", false);
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(() => { void connect(); }, Math.min(1000 * 2 ** failures++, 5000));
    };

    rfb.addEventListener("connect", () => {
      if (disposed || client !== rfb) return;
      failures = 0;
      reportError("");
      reportConnection("connected", false);
      sizeSync = displaySizeSync(
        async (size) => {
          const reply = await api(desktop ? "/claude-desktop/resize" : "/browser/resize", size);
          if (size.scale !== undefined && reply?.scale !== size.scale && followScreen) {
            followScreen = false;
            resize();
          }
        },
        {
          onError: () => reportConnection("connected", true),
          onSuccess: () => reportConnection("connected", false),
        },
      );
      const resize = () => {
        if (disposed || client !== rfb) return;
        sizeSync.update(
          displayGeometry(stage.getBoundingClientRect(), {
            desktop,
            zoom: state.zoom,
            density,
            pixelRatio: followScreen ? window.devicePixelRatio : undefined,
          }),
        );
      };
      resizeObserver = new ResizeObserver(resize);
      resizeObserver.observe(stage);
      unwatchDensity = watchPixelRatio(resize);
      resize();
      rfb._cageResize = resize;

      const preparePaste = () => {
        const id = state.tabId;
        return prepareClipboardPaste(desktop ? "desktop" : "browser", id, {
          valid: () => !disposed && state.active && state.tabId === id,
        });
      };

      input = installRemoteInput(
        {
          events: rfb,
          setKeyboardTarget: (target) => rfb.cageSetKeyboardTarget(target),
          sendKey: (...key) => rfb.sendKey(...key),
          focus: () => rfb.focus(),
          pasteText: (text) => {
            if (desktop || state.systemClipboard) return rfb.cagePasteText(text);
            const prepared = preparePaste();
            return rfb.cageRunInput(() => prepared.send({ text })).finally(() => prepared.dispose());
          },
          runKeyAction: (action) => rfb.cageRunInput(action),
        },
        stage,
        {
          guestPlatform: () => state.guestPlatform,
          systemClipboard: () => state.systemClipboard,
          sharedClipboard: () => state.sharedClipboard,
          copyText: !desktop
            ? (_sendCopy, valid, { id, context, key }) => {
                if (state.systemClipboard !== false) return undefined;
                if (!valid()) throw new Error("复制位置已切换，请重新复制");
                return api("/browser/" + (key === "x" ? "cut" : "copy"), { id, context });
              }
            : undefined,
          setCopyContext: desktop
            ? undefined
            : (context, valid) =>
                rfb.cageRunInput(async () => {
                  if (!valid()) return;
                  try {
                    await api("/browser/copy-context", { id: state.tabId, context });
                  } catch {
                    if (valid()) {
                      input?.cancelCopy();
                      reportStatus("复制按钮同步暂未就绪，可使用复制快捷键或文本传递");
                    }
                  }
                }),
          preparePaste,
          pasteContent: (content, prepared) => {
            const id = state.tabId;
            const valid = () => !disposed && state.active && state.tabId === id;
            return rfb.cagePasteContent(
              content,
              (payload) => {
                if (!valid()) throw new Error("输入目标已切换，请重新粘贴图片");
                return payload.files || payload.fileSelection
                  ? sendFiles(payload, desktop ? "desktop" : "browser", id, { valid })
                  : sendImage(payload, desktop ? "desktop" : "browser", id, { prepared });
              },
              valid,
              !desktop && !state.systemClipboard ? (text) => prepared.send({ text }) : undefined,
            );
          },
          insertText: (text) => {
            if (desktop) {
              if (!state.sharedClipboard) return rfb.cagePasteText(text);
              return rfb.cageRunInput(async () => {
                const valid = () => !disposed && state.active;
                const s = await api("/input", { target: "desktop", id: "desktop", mode: "status" });
                if (!s.available || !valid()) throw new Error("输入位置已失去焦点，请重新点击输入框");
                const session = crypto.randomUUID().replaceAll("-", "");
                const begun = await api("/input", { target: "desktop", id: "desktop", mode: "begin", context: s.context, session, sequence: 0 });
                if (!begun.available || !valid()) throw new Error("输入位置已失去焦点，请重新点击输入框");
                const result = await api("/input", { target: "desktop", id: "desktop", mode: "commit", session, sequence: 1, text });
                if (!result.available) throw new Error("文字输入未完成，请重新点击输入框");
              });
            }
            const id = state.tabId;
            return rfb.cageRunInput(() => {
              if (disposed || !state.active || state.tabId !== id) throw new Error("输入位置已切换，请重新输入");
              return api("/browser/insert-text", { id, text });
            });
          },
          insertComposition: (id, value) =>
            rfb.cageRunInput(async () => {
              if (value.mode !== "cancel" && (disposed || !state.active || (desktop ? "desktop" : state.tabId) !== id)) {
                throw new Error("输入位置已切换，请重新输入");
              }
              const result = await api("/input", { target: desktop ? "desktop" : "browser", id, ...value });
              if (value.mode !== "cancel" && !result.available) throw new Error("输入位置已失去焦点，请重新输入");
              return result;
            }),
          tabId: () => (desktop ? "desktop" : state.tabId),
          caretRect: async (session) => {
            const id = desktop ? "desktop" : state.tabId;
            const value = await api("/input", { target: desktop ? "desktop" : "browser", id, mode: "status", session });
            return !disposed && state.active && (desktop ? "desktop" : state.tabId) === id ? value : null;
          },
          mode: () => state.keyboard,
          isActive: () => state.active,
          onStatus: reportStatus,
          onFallback: reportFallback,
        },
      );
      if (state.active) rfb.focus();
    });

    rfb.addEventListener("disconnect", retry);
    rfb.addEventListener("securityfailure", (e) => {
      reportError(e?.detail?.reason || "VNC 认证失败");
      retry();
    });
  } catch (err) {
    if (disposed) return;
    reportError(err?.message || String(err));
    reportConnection("disconnected", false);
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => { void connect(); }, Math.min(1000 * 2 ** failures++, 5000));
  }
}

window.cageVNC = {
  configure(next = {}) {
    const prevTab = state.tabId;
    const prevMode = state.mode;
    const prevConn = state.connectionId;
    const prevZoom = state.zoom;
    Object.assign(state, next);
    setContext({ connection_id: state.connectionId });
    if (next.tabId !== undefined && next.tabId !== prevTab) input?.cancelCopy();
    if (next.zoom !== undefined && next.zoom !== prevZoom) client?._cageResize?.();
    if (next.mode !== undefined && next.mode !== prevMode || next.connectionId !== undefined && next.connectionId !== prevConn) {
      failures = 0;
      void connect();
    }
  },
  focus() { client?.focus(); },
  edit(action) { return input?.focused() ? input.edit(action) : false; },
  copy() { input?.copy(); },
  paste() { input?.readAndPaste(); },
  pasteText(text) { input?.paste(text); },
  receiveCopy(value) { input?.receiveCopy(value); },
  retrySize() { sizeSync?.retry(); },
  reconnect() { failures = 0; void connect(); },
};

(async () => {
  await sessionReady;
  await connect();
})();
