(() => {
  const post = () => window.parent.postMessage({
    type: "breakfix:document-location",
    source: "kubernetes",
    version: "snapshot-ce98a43",
    locale: "en",
    path: window.location.pathname,
    hash: window.location.hash,
  }, "http://localhost:5173");
  post();
  window.addEventListener("hashchange", post);
  window.addEventListener("popstate", post);
})();
