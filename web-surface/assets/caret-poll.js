export const caretPollFast = 250;
export const caretPollSlow = 2000;

export function caretPollDelay(quiet) {
  if (quiet < 3000) return caretPollFast;
  if (quiet < 8000) return 500;
  if (quiet < 20000) return 1000;
  return caretPollSlow;
}

export const caretFreshFor = caretPollSlow + 500;
