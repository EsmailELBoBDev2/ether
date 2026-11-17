const M3U_HEADER_ATTR = /([a-zA-Z0-9_-]+)=(?:"([^"]*)"|'([^']*)')/g;

function isHttpUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (u.protocol === "http:" || u.protocol === "https:") && Boolean(u.hostname);
  } catch {
    return false;
  }
}

/** Reports whether an M3U document's #EXTM3U header carries x-tvg-url or url-tvg. */
export function hasM3UHeaderGuide(raw: string): boolean {
  for (const rawLine of raw.split(/\r?\n/)) {
    const line = rawLine.replace(/^\ufeff/, "").trim();
    if (!line) continue;
    if (!line.startsWith("#EXTM3U")) return false;
    M3U_HEADER_ATTR.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = M3U_HEADER_ATTR.exec(line)) !== null) {
      const key = (m[1] ?? "").toLowerCase();
      if (key !== "x-tvg-url" && key !== "url-tvg") continue;
      const val = m[2] ?? m[3] ?? "";
      for (const part of val.split(",")) {
        const candidate = part.trim();
        if (candidate && isHttpUrl(candidate)) return true;
      }
    }
    return false;
  }
  return false;
}
