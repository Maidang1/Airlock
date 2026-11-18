import { api, callBridge, mygoAvailable } from "./bridge.js";

const MAX_IMAGE_BYTES = 20 * 1024 * 1024;

export function hasFiles(dataTransfer) {
  if (!dataTransfer) return false;
  if (dataTransfer.types && Array.from(dataTransfer.types).includes("Files")) {
    return true;
  }
  return Boolean(dataTransfer.files && dataTransfer.files.length > 0);
}

export function transferFiles(dataTransfer) {
  if (!dataTransfer) return [];
  if (dataTransfer.files && dataTransfer.files.length > 0) {
    return Array.from(dataTransfer.files);
  }
  if (dataTransfer.items) {
    return Array.from(dataTransfer.items)
      .filter((item) => item.kind === "file")
      .map((item) => item.getAsFile())
      .filter(Boolean);
  }
  return [];
}

function arrayBufferToBase64(buffer) {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

export async function imagePayload(file) {
  if (!file) throw new Error("剪贴板中没有可用图片");
  if (file.type === "image/png") {
    const buf = await file.arrayBuffer();
    if (buf.byteLength > MAX_IMAGE_BYTES) {
      throw new Error("图片不能超过 20 MB");
    }
    return { png: arrayBufferToBase64(buf) };
  }
  const bitmap = await createImageBitmap(file);
  try {
    const canvas = document.createElement("canvas");
    canvas.width = bitmap.width;
    canvas.height = bitmap.height;
    const ctx = canvas.getContext("2d");
    ctx.drawImage(bitmap, 0, 0);
    const pngBlob = await new Promise((resolve, reject) => {
      canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error("图片编码失败"))), "image/png");
    });
    const buf = await pngBlob.arrayBuffer();
    if (buf.byteLength > MAX_IMAGE_BYTES) {
      throw new Error("图片不能超过 20 MB");
    }
    return { png: arrayBufferToBase64(buf) };
  } finally {
    bitmap.close?.();
  }
}

export async function readClipboardContent() {
  if (mygoAvailable()) {
    const res = await callBridge("ClipboardReadContent");
    if (res && res.kind === "image" && res.png) {
      return { png: res.png };
    }
    if (res && res.kind === "files" && (res.id || res.files)) {
      return { fileSelection: res.id || res.files };
    }
    if (res && res.kind === "text" && typeof res.text === "string") {
      return { text: res.text };
    }
    if (res && res.kind === "error") {
      throw new Error(res.error || "剪贴板读取失败");
    }
    throw new Error("剪贴板中没有可粘贴的图片、文件或文字");
  }
  const text = await navigator.clipboard?.readText?.();
  if (typeof text === "string" && text.length > 0) {
    return { text };
  }
  throw new Error("剪贴板中没有可粘贴的内容");
}

export function prepareClipboardPaste(target, id, { valid } = {}) {
  let transferPromise;
  let cancelled = false;
  const start = async () => {
    if (valid && !valid()) throw new Error("输入目标已切换");
    const res = await api("/input/paste/start", { target, id });
    if (cancelled) {
      if (res?.transfer || res?.context) {
        void api("/input/paste/cancel", {
          target,
          id,
          transfer: res.transfer || "",
          context: res.context || "",
        }).catch(() => {});
      }
      throw new Error("粘贴已取消");
    }
    return res || {};
  };
  transferPromise = start();
  return {
    async send(payload) {
      const prep = await transferPromise;
      if (valid && !valid()) throw new Error("输入目标已切换");
      if (typeof payload?.text === "string") {
        return api("/input/text", {
          target,
          id,
          text: payload.text,
          transfer: prep.transfer || "",
          context: prep.context || "",
        });
      }
      if (typeof payload?.png === "string") {
        return api("/input/image", {
          target,
          id,
          png: payload.png,
          transfer: prep.transfer || "",
          context: prep.context || "",
        });
      }
      throw new Error("不支持的粘贴内容");
    },
    dispose() {
      cancelled = true;
      transferPromise
        ?.then((prep) => {
          if (prep?.transfer || prep?.context) {
            return api("/input/paste/cancel", {
              target,
              id,
              transfer: prep.transfer || "",
              context: prep.context || "",
            });
          }
          return undefined;
        })
        .catch(() => {});
    },
  };
}

export async function sendImage(payloadOrPromise, target, id, { prepared } = {}) {
  const payload = await payloadOrPromise;
  if (!payload || typeof payload.png !== "string") {
    throw new Error("没有可发送的图片内容");
  }
  if (prepared) {
    return prepared.send({ png: payload.png });
  }
  return api("/input/image", { target, id, png: payload.png });
}
