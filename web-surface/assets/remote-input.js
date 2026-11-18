import { getKeycode, getKeysym } from "/novnc/core/input/util.js";
import { api, callBridge, getContext, mygoAvailable } from "./bridge.js";
import { caretPollDelay, caretPollFast, caretFreshFor } from "./caret-poll.js";
import { readClipboardContent, transferFiles, hasFiles, imagePayload } from "./image-transfer.js";

export function installRemoteInput(
  transport,
  container,
  {
    mode,
    guestPlatform = () => "linux",
    systemClipboard = () => true,
    sharedClipboard = () => false,
    onStatus,
    onFallback,
    isActive,
    insertText,
    insertComposition,
    caretRect,
    surface,
    inputLabel = "Cage 网页输入",
    tabId: getTargetId = () => "",
    pasteContent,
    preparePaste,
    setCopyContext,
    copyText,
  },
) {
  const listeners = [];
  const handled = new Set();
  const physical = new Set();
  const remote = new Map();
  let lastText = "";
  let copyRequested = false;
  let copyContext = "";
  let copyRevision;
  let copyTimer;
  let pending = 0;
  let composing = false;
  let compositionTarget = "";
  let compositionSession = "";
  let compositionSequence = 0;
  let caretState;
  let caretTimer;
  let caretWait = caretPollFast;
  let lastAction = Date.now();
  let caretPending = false;
  let disposed = false;

  const target = document.createElement("textarea");
  target.setAttribute("aria-label", inputLabel);
  target.setAttribute("autocomplete", "off");
  target.setAttribute("autocapitalize", "off");
  target.setAttribute("wrap", "off");
  target.spellcheck = false;
  target.className = "remote-text-input";
  target.style.cssText = "position:fixed;left:0;top:0";
  container.append(target);
  transport.setKeyboardTarget(target);

  const on = (object, type, fn, options) => {
    object.addEventListener(type, fn, options);
    listeners.push(() => object.removeEventListener(type, fn, options));
  };
  const focused = () =>
    isActive() && (container.contains(document.activeElement) || document.activeElement === target);
  const macGuest = () => ["darwin", "macos"].includes(guestPlatform());
  const windowsGuest = () => ["windows", "windows-11"].includes(guestPlatform());
  const isSharedClipboard = () => systemClipboard() && sharedClipboard();
  const shortcutModifier = () => {
    if (macGuest()) return physical.has("MetaRight") ? [0xffec, "MetaRight"] : [0xffeb, "MetaLeft"];
    return physical.has("ControlRight") ? [0xffe4, "ControlRight"] : [0xffe3, "ControlLeft"];
  };
  const cancelCopy = () => {
    copyContext = "";
    copyRevision = undefined;
    copyRequested = false;
    clearTimeout(copyTimer);
  };
  const prepareCopy = () => {
    if (!isActive() || disposed) return;
    if (isSharedClipboard()) {
      if (copyContext) cancelCopy();
      return;
    }
    copyContext = crypto.randomUUID().replaceAll("-", "");
    copyRevision = mygoAvailable() ? callBridge("ClipboardRevision").catch(() => undefined) : Promise.resolve(undefined);
    const context = copyContext;
    void setCopyContext?.(context, () => !disposed && isActive() && context === copyContext).catch(() => {
      if (copyContext === context) cancelCopy();
    });
  };
  const position = (x, y) => {
    target.style.left = `${Math.max(0, Math.min(x, window.innerWidth - 1))}px`;
    target.style.top = `${Math.max(0, Math.min(y, window.innerHeight - 24))}px`;
  };
  const syncCaret = async (cursorOnly = false) => {
    if (!caretRect || caretPending || disposed || !isActive()) return;
    caretPending = true;
    const id = getTargetId();
    try {
      const value = await caretRect(compositionSession);
      if (disposed || !isActive() || id !== getTargetId()) return;
      if (["text", "pointer", "default"].includes(value?.cursor)) {
        const canvas = surface?.() || container.querySelector("canvas");
        canvas?.style?.setProperty("cursor", value.cursor, "important");
      }
      if (cursorOnly) return;
      caretState = value?.available ? { ...value, target: id, time: Date.now() } : null;
      if (!value?.available || !value.rect || value.rect[3] <= 0) return;
      const canvas = surface?.() || container.querySelector("canvas");
      const width = value.width || canvas?.width;
      const imageHeight = value.height || canvas?.height;
      if (!width || !imageHeight) return;
      const bounds = canvas.getBoundingClientRect();
      const height = Math.max(1, (value.rect[3] / imageHeight) * bounds.height);
      target.style.height = `${height}px`;
      target.style.lineHeight = `${height}px`;
      const x = bounds.left + (value.rect[0] / width) * bounds.width;
      const y = bounds.top + (value.rect[1] / imageHeight) * bounds.height;
      position(x, y);
    } catch {
    } finally {
      caretPending = false;
    }
  };
  const pollCaret = async () => {
    if (focused()) await syncCaret();
    if (disposed) return;
    caretWait = caretPollDelay(Date.now() - lastAction);
    caretTimer = setTimeout(pollCaret, caretWait);
  };
  if (caretRect) caretTimer = setTimeout(pollCaret, 0);
  const acted = () => {
    lastAction = Date.now();
    if (!caretRect || disposed || caretWait <= caretPollFast) return;
    clearTimeout(caretTimer);
    caretWait = caretPollFast;
    caretTimer = setTimeout(pollCaret, caretWait);
  };
  const bounds = container.getBoundingClientRect();
  position(bounds.left + 12, bounds.top + 12);

  let lastHoverProbe = -Infinity;
  let hoverTimer;
  let hovered = false;
  const probeHover = () => {
    if (!windowsGuest() || !hovered || !isActive() || disposed || document.visibilityState === "hidden" || !document.hasFocus()) return;
    if (caretPending) { hoverTimer = setTimeout(probeHover, caretPollFast); return; }
    lastHoverProbe = Date.now();
    void syncCaret(true);
  };
  listeners.push(() => clearTimeout(hoverTimer));
  on(container, "pointerleave", () => { hovered = false; clearTimeout(hoverTimer); });
  on(container, "pointermove", () => {
    if (!windowsGuest() || !isActive() || disposed || document.visibilityState === "hidden" || !document.hasFocus()) return;
    acted();
    hovered = true;
    clearTimeout(hoverTimer);
    hoverTimer = setTimeout(probeHover, Math.max(0, caretPollFast - (Date.now() - lastHoverProbe)));
  });
  on(
    container,
    "pointerdown",
    (event) => {
      acted();
      if (composing) {
        if (compositionSession) queueComposition({ mode: "cancel" });
        resetComposition();
        target.blur();
      }
      position(event.clientX, event.clientY);
      if (event.isTrusted && event.button === 0) prepareCopy();
    },
    true,
  );
  const say = (text) => {
    if (!disposed) onStatus(text);
  };
  const commit = (text) => {
    if (disposed || !isActive() || !text) return;
    insertText(text).catch(() => {
      if (!disposed) onStatus("文字输入未完成，请重新连接画面后重试");
    });
  };
  const queueComposition = (value) => {
    const targetId = compositionTarget;
    if (!insertComposition || !compositionSession || !targetId || disposed || (!isActive() && value.mode !== "cancel")) return;
    insertComposition(targetId, { session: compositionSession, sequence: compositionSequence++, ...value }).catch((error) => {
      if (!disposed && value.mode !== "cancel") say(error.message || "输入法组合文字未能同步");
    });
  };
  const resetComposition = () => {
    composing = false;
    compositionTarget = "";
    compositionSession = "";
    target.value = "";
  };
  const startComposition = () => {
    acted();
    composing = true;
    compositionTarget = getTargetId();
    if (insertComposition && caretState?.context && caretState.target === compositionTarget && Date.now() - caretState.time < caretFreshFor) {
      compositionSession = crypto.randomUUID().replaceAll("-", "");
      compositionSequence = 0;
      queueComposition({ mode: "begin", context: caretState.context });
    }
    void syncCaret();
  };
  on(target, "compositionstart", startComposition);
  on(target, "compositionupdate", () => { void syncCaret(); });
  const endComposition = (text) => {
    if (composing) {
      if (compositionSession) queueComposition({ mode: "commit", text, selectionStart: 0, selectionEnd: 0 });
      else if (compositionTarget === getTargetId()) commit(text);
    }
    resetComposition();
    void syncCaret();
  };
  on(target, "compositionend", (event) => endComposition(event.data || ""));
  on(target, "input", (event) => {
    if (document.activeElement !== target) {
      if (!composing) target.value = "";
      return;
    }
    if (composing || event.isComposing) {
      if (compositionSession && ["insertCompositionText", "insertFromComposition"].includes(event.inputType)) {
        queueComposition({
          mode: "update",
          text: target.value,
          selectionStart: target.selectionStart ?? target.value.length,
          selectionEnd: target.selectionEnd ?? target.value.length,
        });
      }
      return;
    }
    if (!["insertFromComposition", "insertCompositionText"].includes(event.inputType)) commit(target.value);
    target.value = "";
  });
  on(target, "blur", () => {
    if (composing && compositionSession) queueComposition({ mode: "cancel", text: "", selectionStart: 0, selectionEnd: 0 });
    resetComposition();
  });
  on(document, "focusin", (event) => {
    if (!container.contains(event.target)) cancelCopy();
  });
  on(document, "pointerdown", (event) => {
    if (!container.contains(event.target)) cancelCopy();
  }, true);
  on(document, "visibilitychange", () => {
    if (document.visibilityState === "hidden") cancelCopy();
  });
  on(container, "pointerup", () => {
    acted();
    setTimeout(() => void syncCaret(), 50);
  });
  on(container, "wheel", acted, { capture: true, passive: true });

  const shortcutKeys = (key, shift = false) => {
    const modifier = shortcutModifier();
    const keys = [[...modifier, true]];
    if (shift) keys.push([0xffe1, "ShiftLeft", true]);
    keys.push([key.charCodeAt(0), "Key" + key.toUpperCase(), true], [key.charCodeAt(0), "Key" + key.toUpperCase(), false]);
    if (shift && !physical.has("ShiftLeft")) keys.push([0xffe1, "ShiftLeft", false]);
    if (!physical.has(modifier[1])) keys.push([...modifier, false]);
    return keys;
  };
  const chord = (key, shift = false) => shortcutKeys(key, shift).forEach((value) => transport.sendKey(...value));

  const receiveCopy = async ({ id, text, clipboard, context, document: sourceDocument, error }) => {
    if (isSharedClipboard()) return;
    const valid = () =>
      !disposed && Boolean(copyContext) && context === copyContext && id === getTargetId() && focused() &&
      document.visibilityState !== "hidden" && document.hasFocus();
    if (!valid()) return;
    if (error) { copyRequested = false; clearTimeout(copyTimer); say("复制未完成，请重新选择内容后重试。"); return; }
    const revision = copyRevision;
    let content = { text };
    try {
      const expected = await revision;
      if (!valid()) return;
      if (clipboard) {
        const limit = clipboard.mime === "image/png" ? 20 * 1024 * 1024 : 2 * 1024 * 1024;
        if (!["text/plain", "image/png"].includes(clipboard.mime) || !Number.isInteger(clipboard.size) || clipboard.size < 0 || clipboard.size > limit) return;
        content = await api("/browser/clipboard", { id: clipboard.id, tab: id, context, document: sourceDocument });
        if (!valid()) return;
      }
      if (typeof content.text === "string") {
        if (new TextEncoder().encode(content.text).length > 2 * 1024 * 1024) return;
        lastText = content.text;
      } else if (typeof content.png !== "string" || content.png.length > 28 * 1024 * 1024) return;
      copyRequested = false;
      clearTimeout(copyTimer);
      if (mygoAvailable()) {
        if (typeof expected !== "number") throw new Error("无法确认本机剪贴板版本，请重新复制。");
        const result = await callBridge("ClipboardSyncWrite", getContext().connection_id, { ...content, revision: expected });
        if (result?.superseded) { if (valid()) say("本机剪贴板已更新，请重新复制客机内容。"); return; }
      } else if (typeof content.text === "string") {
        await navigator.clipboard.writeText(content.text);
      }
      if (valid()) say("已复制到本机");
    } catch (err) {
      if (!valid()) return;
      if (typeof content.text === "string") onFallback(content.text, "请在这里复制到本机。");
      else say(err.message || "复制内容读取失败，请重试。");
    }
  };
  const waitForCopy = () => {
    if (!copyRequested) return;
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => {
      copyRequested = false;
      onFallback(lastText, "未收到新的选中文本，可在这里复制 Cage 剪贴板。");
    }, 1600);
  };
  const copy = (key = "c") => {
    if (disposed || !isActive()) return;
    copyRequested = true;
    say("正在复制所选文本…");
    clearTimeout(copyTimer);
    if (isSharedClipboard()) { chord(key); copyRequested = false; return; }
    prepareCopy();
    if (copyText) {
      const context = copyContext;
      const id = getTargetId();
      const valid = () =>
        !disposed && isActive() && context === copyContext && id === getTargetId() &&
        document.visibilityState !== "hidden" && document.hasFocus();
      return transport.runKeyAction(async (sendKey) => {
        const before = await caretRect?.();
        const sendCopy = async () => {
          if (!valid()) throw new Error("复制窗口已变化，请重新选择文本。");
          for (const value of shortcutKeys(key)) await sendKey(...value);
        };
        const content = await copyText(sendCopy, valid, { id, context, key });
        if (content === undefined) { await sendCopy(); waitForCopy(); return; }
        const after = await caretRect?.();
        if (before?.available && (!after?.available || before.context !== after.context)) throw new Error("复制位置已变化，请重新选择文本。");
        if (valid()) await receiveCopy({ ...content, id, context });
      }).catch((err) => { if (valid()) say(err.message || "未收到新的选中文本，请重新选择后复制。"); });
    }
    chord(key);
    waitForCopy();
  };
  const paste = (text) => {
    if (disposed) return;
    pending++;
    say("正在粘贴…");
    transport.focus();
    transport.pasteText(text).then(
      () => { pending--; say(pending ? "正在粘贴…" : "已粘贴"); },
      () => { pending--; say("粘贴连接未就绪，请重新连接画面后重试"); },
    );
  };
  const receive = (content, preparation = preparePaste?.()) => {
    if (disposed || !isActive()) { preparation?.dispose(); return; }
    pending++;
    say("正在传递剪贴板内容…");
    transport.focus();
    return pasteContent(content, preparation).then(
      () => { pending--; say(pending ? "正在粘贴…" : "已传递到当前输入位置"); },
      (err) => {
        pending--;
        say(err.message || "图片或文本未能传入，请重新连接后重试");
      },
    ).finally(() => preparation?.dispose());
  };
  const readAndPaste = () => {
    if (disposed || !isActive()) return;
    if (isSharedClipboard()) {
      const id = getTargetId();
      say("正在粘贴…");
      return transport.runKeyAction(async (sendKey) => {
        const before = await caretRect?.();
        await callBridge("SyncGuestClipboard", getContext().connection_id);
        const after = await caretRect?.();
        if (disposed || !isActive() || id !== getTargetId() || (before?.available && (!after?.available || before.context !== after.context))) {
          throw new Error("粘贴位置已切换，请回到原输入位置重试");
        }
        for (const key of shortcutKeys("v")) await sendKey(...key);
        say("已粘贴");
      }).catch((err) => say(err.message || "剪贴板尚未同步，请重新复制后重试"));
    }
    const preparation = preparePaste?.();
    return receive(readClipboardContent(), preparation);
  };
  on(transport.events, "clipboard", async (event) => {
    lastText = event.detail.text;
    if (isSharedClipboard()) { copyRequested = false; clearTimeout(copyTimer); return; }
    if (copyRequested) await receiveCopy({ id: getTargetId(), text: lastText, context: copyContext });
  });
  const release = () => {
    cancelCopy();
    for (const [code, keysym] of remote) transport.sendKey(keysym, code, false);
    remote.clear();
    physical.clear();
    handled.clear();
  };
  on(window, "blur", release);
  on(
    document,
    "keydown",
    (event) => {
      if (!focused()) return;
      acted();
      if (event.isTrusted && (!copyContext || ["Enter", " ", "Tab"].includes(event.key) || event.ctrlKey || event.metaKey || event.altKey)) {
        prepareCopy();
      }
      if (composing || event.isComposing || event.keyCode === 229 || ["Process", "Dead"].includes(event.key)) {
        event.stopImmediatePropagation();
        return;
      }
      const code = getKeycode(event);
      const mapping = mode() === "mac" && !macGuest();
      const malformedCommandLetter = event.metaKey && /^Key[A-Z]$/.test(code) && !/^[a-z]$/i.test(event.key);
      const key = malformedCommandLetter ? code[3].toLowerCase() : event.key.toLowerCase();
      if (["Shift", "Control", "Alt", "Meta"].includes(event.key)) physical.add(code);
      if (event.key === "Shift") {
        const sym = getKeysym(event);
        transport.sendKey(sym, code, true);
        remote.set(code, sym);
        event.preventDefault();
        event.stopImmediatePropagation();
        return;
      }
      if (event.key === "Enter" && event.shiftKey && !event.metaKey && !event.ctrlKey && !event.altKey) {
        const shift = physical.has("ShiftRight") ? [0xffe2, "ShiftRight"] : [0xffe1, "ShiftLeft"];
        const sym = getKeysym(event);
        if (!remote.has(shift[1])) transport.sendKey(...shift, true);
        transport.sendKey(sym, code, true);
        transport.sendKey(sym, code, false);
        if (physical.has(shift[1])) remote.set(shift[1], shift[0]);
        else transport.sendKey(...shift, false);
        handled.add(code);
        event.preventDefault();
        event.stopImmediatePropagation();
        return;
      }
      if (mapping && event.metaKey && ["Shift", "Control", "Alt"].includes(event.key)) {
        const sym = getKeysym(event);
        transport.sendKey(sym, code, true);
        remote.set(code, sym);
        event.preventDefault();
        event.stopImmediatePropagation();
        return;
      }
      const clipboard = (event.metaKey || (!macGuest() && event.ctrlKey)) && !event.altKey && ((!event.shiftKey && ["c", "x"].includes(key)) || key === "v");
      if (clipboard) {
        event.stopImmediatePropagation();
        handled.add(code);
        if (key === "v") {
          if (mygoAvailable()) {
            event.preventDefault();
            void readAndPaste();
          } else {
            target.value = "";
            target.focus({ preventScroll: true });
          }
        } else {
          event.preventDefault();
          copy(key);
        }
        return;
      }
      if ((mapping || macGuest()) && event.metaKey && !["Meta", "Shift", "Control", "Alt", "CapsLock"].includes(event.key)) {
        let sym = getKeysym(event);
        if ((malformedCommandLetter || sym === null || sym < 32) && /^Key[A-Z]$/.test(code)) {
          sym = (event.shiftKey ? code[3] : code[3].toLowerCase()).codePointAt(0);
        }
        if (sym !== null) {
          const held = (p) => [...physical].some((c) => c.startsWith(p));
          const modifier = shortcutModifier();
          const shift = physical.has("ShiftRight") ? [0xffe2, "ShiftRight"] : [0xffe1, "ShiftLeft"];
          transport.sendKey(...modifier, true);
          if (event.shiftKey) transport.sendKey(...shift, true);
          transport.sendKey(sym, code, true);
          transport.sendKey(sym, code, false);
          if (event.shiftKey && !held("Shift")) transport.sendKey(...shift, false);
          if (!held(macGuest() ? "Meta" : "Control")) transport.sendKey(...modifier, false);
          event.preventDefault();
          event.stopImmediatePropagation();
          handled.add(code);
          return;
        }
      }
      if (mapping && event.key === "Meta") event.stopImmediatePropagation();
      if (!event.metaKey && !event.ctrlKey && [...event.key].length === 1 && event.key.codePointAt(0) > 127) {
        event.preventDefault();
        event.stopImmediatePropagation();
        handled.add(code);
        commit(event.key);
      } else if (!event.metaKey && !event.ctrlKey && !event.altKey && event.key.length === 1 && /[^a-zA-Z0-9 ]/.test(event.key)) {
        event.stopImmediatePropagation();
      } else if (macGuest()) {
        const sym = getKeysym(event);
        if (sym !== null) { transport.sendKey(sym, code, true); remote.set(code, sym); }
        event.preventDefault();
        event.stopImmediatePropagation();
      }
    },
    true,
  );
  on(
    document,
    "keyup",
    (event) => {
      const code = getKeycode(event);
      physical.delete(code);
      if (remote.has(code)) {
        transport.sendKey(remote.get(code), code, false);
        remote.delete(code);
        event.stopImmediatePropagation();
        return;
      }
      if (handled.delete(code) || (focused() && mode() === "mac" && event.key === "Meta")) {
        event.stopImmediatePropagation();
      } else if (focused() && !composing && caretRect) {
        setTimeout(() => void syncCaret(), 35);
      }
    },
    true,
  );
  on(
    document,
    "paste",
    (event) => {
      if (!isActive() || (!focused() && event.target !== target)) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      const files = transferFiles(event.clipboardData);
      if (files.length === 1 && files[0].type.startsWith("image/")) {
        const preparation = preparePaste?.();
        receive(imagePayload(files[0]), preparation);
      } else if (files.length) {
        receive({ files, kind: "paste" });
      } else if (event.clipboardData.types.includes("text/plain")) {
        paste(event.clipboardData.getData("text/plain"));
      } else say("剪贴板中没有可粘贴的图片或文本");
    },
    true,
  );
  on(container, "dragover", (event) => {
    if (!isActive() || !hasFiles(event.dataTransfer)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
  });
  on(container, "drop", (event) => {
    if (!isActive() || !hasFiles(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    const canvas = surface?.() || container.querySelector("canvas") || container;
    const rect = canvas.getBoundingClientRect();
    receive({
      files: transferFiles(event.dataTransfer),
      kind: "drop",
      x: Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width)),
      y: Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height)),
    });
  });

  return {
    copy,
    cancelCopy,
    receiveCopy,
    readAndPaste,
    paste,
    text: () => lastText,
    edit(action) {
      if (composing) return false;
      if (action === "copy") copy();
      else if (action === "cut") copy("x");
      else if (action === "paste") void readAndPaste();
      else if (action === "selectAll") chord("a");
      else if (action === "undo") chord("z");
      else if (action === "redo") chord("Z", true);
      else return false;
      return true;
    },
    focused,
    dispose() {
      if (composing && compositionSession) {
        queueComposition({ mode: "cancel", text: "", selectionStart: 0, selectionEnd: 0 });
      }
      disposed = true;
      clearTimeout(copyTimer);
      clearTimeout(caretTimer);
      release();
      listeners.forEach((remove) => remove());
      transport.setKeyboardTarget(null);
      target.remove();
    },
  };
}
