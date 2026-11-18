import { api, callBridge, mygoAvailable } from "./bridge.js";

export async function sendFiles(payloadOrPromise, target, id, { valid } = {}) {
  const payload = await payloadOrPromise;
  if (valid && !valid()) {
    throw new Error("文件上传目标已切换");
  }
  if (payload?.fileSelection && mygoAvailable()) {
    return callBridge("UploadSelectedFiles", target, id, String(payload.fileSelection));
  }
  const files = payload?.files || [];
  if (!files.length) {
    throw new Error("未选择要传递的文件");
  }
  const kind = payload?.kind === "drop" ? "drop" : "paste";
  const started = await api("/input/files/start", {
    target,
    id,
    kind,
    x: payload?.x,
    y: payload?.y,
    names: files.map((f) => f.name),
  });
  const transferId = started?.id || id;
  if (!transferId) {
    throw new Error("无法启动文件传输");
  }
  try {
    const uploadedPaths = [];
    for (const file of files) {
      if (valid && !valid()) {
        throw new Error("文件上传已取消");
      }
      const query = new URLSearchParams({ name: file.name || "file" });
      const res = await api(`/input/files/${encodeURIComponent(target)}/${encodeURIComponent(transferId)}?${query.toString()}`, file, {
        rawBody: true,
        headers: { "Content-Type": "application/octet-stream" },
      });
      if (res?.path) uploadedPaths.push(res.path);
    }
    await api("/input/files/finish", { target, id: transferId });
    return { ok: true, count: files.length, paths: uploadedPaths };
  } catch (err) {
    void api("/input/files/cancel", { target, id: transferId }).catch(() => {});
    throw err;
  }
}
