export function displayGeometry({ width, height }, { desktop = false, zoom = 1, density = 2, pixelRatio } = {}) {
  if (width < (desktop ? 320 : 160) || height < 240) return null;
  const follow = pixelRatio > 0;
  const automatic = desktop && zoom === "auto" && !follow;
  const zoomFactor = Number.isFinite(zoom) && zoom > 0 ? zoom : 1;
  let scale = !follow && desktop && Number.isFinite(density) && density >= 1 && density <= 8
    ? density : density === 1.5 ? 1.5 : 2;
  let ratio = scale / zoomFactor;
  if (follow) ratio = Math.max(1, Math.max(1, Math.min(2, pixelRatio)) * scale / 2);
  const limit = Math.min(1, 8192 / (width * ratio), 8192 / (height * ratio),
    automatic ? Math.sqrt(12_000_000 / (width * height * ratio * ratio)) : 1);
  if (follow) scale = Math.round(Math.max(0.75, Math.min(4, ratio * limit * zoomFactor)) * 64) / 64;
  return {
    width: Math.max(desktop ? 320 : Math.ceil(160 * scale), Math.floor(width * ratio * limit)),
    height: Math.max(desktop ? 240 : Math.ceil(240 * scale), Math.floor(height * ratio * limit)),
    ...(follow ? { scale } : {}),
  };
}

export function watchPixelRatio(changed, view = window) {
  let query;
  let stopped = false;
  const update = () => {
    if (stopped) return;
    listen();
    changed();
  };
  const listen = () => {
    query = view.matchMedia(`(resolution: ${view.devicePixelRatio}dppx)`);
    query.addEventListener?.("change", update, { once: true });
  };
  listen();
  return () => { stopped = true; query?.removeEventListener?.("change", update); };
}

export function displaySizeSync(resize, { onError = () => {}, onSuccess = () => {}, delay = 160 } = {}) {
  let desired;
  let applied = "";
  let timer;
  let busy = false;
  let disposed = false;
  let retries = 0;
  const key = (size) => (size ? `${size.width}x${size.height}@${size.scale ?? ""}` : "");
  const schedule = (ms) => { clearTimeout(timer); timer = setTimeout(apply, ms); };
  async function apply() {
    timer = null;
    if (disposed || busy || !desired || key(desired) === applied) return;
    const target = desired;
    let retryDelay;
    busy = true;
    try {
      await resize(target);
      if (!disposed) {
        applied = key(target);
        if (key(desired) === applied) { retries = 0; onSuccess(); }
      }
    } catch (error) {
      if (!disposed && key(desired) === key(target)) {
        onError(error);
        if (retries < 3) retryDelay = 500 * 2 ** retries++;
      }
    } finally {
      busy = false;
      if (!disposed) {
        if (key(desired) !== key(target)) schedule(delay);
        else if (retryDelay !== undefined) schedule(retryDelay);
      }
    }
  }
  return {
    update(size) {
      if (disposed || !size || key(size) === key(desired)) return;
      desired = size;
      if (!busy && (timer == null || retries > 0)) schedule(delay);
      retries = 0;
    },
    retry() { if (!disposed) { applied = ""; retries = 0; schedule(0); } },
    dispose() { disposed = true; clearTimeout(timer); },
  };
}
