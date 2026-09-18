export const documentationSource = {
  source: "kubernetes",
  version: "snapshot-ce98a43",
  locale: "en",
  entryPath: "/docs/",
} as const;

// The reader keeps the historical URL shape: source, version, path (site-style
// /docs/.../ with slashes), and hash as a query parameter. Paths map onto
// library pages by trimming the surrounding slashes.
export function libraryPathOf(urlPath: string): string {
  return urlPath.replace(/^\/+|\/+$/g, "");
}

export function urlPathOf(libraryPath: string): string {
  return `/${libraryPath}/`;
}
