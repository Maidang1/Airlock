export function installLocalCursorFallback(cursor, enabled) {
  if (!enabled || !cursor) return;
  const change = cursor.change.bind(cursor);
  const clear = cursor.clear.bind(cursor);
  const fallback = () => {
    clear();
    cursor._target.style.cursor = "default";
  };
  cursor.clear = fallback;
  cursor.change = (rgba, hotx, hoty, w, h) => {
    for (let i = 3; i < rgba.length; i += 4) {
      if (rgba[i] !== 0) {
        change(rgba, hotx, hoty, w, h);
        return;
      }
    }
    fallback();
  };
}
