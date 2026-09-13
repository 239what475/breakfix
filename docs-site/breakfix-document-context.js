(function () {
  "use strict";

  var config = {
    parentOrigin: "__BREAKFIX_PARENT_ORIGIN__",
    source: "__BREAKFIX_SOURCE__",
    version: "__BREAKFIX_VERSION__",
    locale: "__BREAKFIX_LOCALE__",
    docsPrefix: "__BREAKFIX_DOCS_PREFIX__"
  };

  if (window.parent === window || !config.parentOrigin) {
    return;
  }

  function isDocumentationPath(path) {
    var prefix = config.docsPrefix;
    return path === prefix.slice(0, -1) || path.indexOf(prefix) === 0;
  }

  function sendLocation() {
    var path = window.location.pathname;
    if (!isDocumentationPath(path)) {
      return;
    }
    window.parent.postMessage({
      type: "breakfix:document-location",
      source: config.source,
      version: config.version,
      locale: config.locale,
      path: path,
      hash: window.location.hash
    }, config.parentOrigin);
  }

  var originalPushState = window.history.pushState;
  window.history.pushState = function () {
    var result = originalPushState.apply(this, arguments);
    window.setTimeout(sendLocation, 0);
    return result;
  };

  var originalReplaceState = window.history.replaceState;
  window.history.replaceState = function () {
    var result = originalReplaceState.apply(this, arguments);
    window.setTimeout(sendLocation, 0);
    return result;
  };

  window.addEventListener("hashchange", sendLocation);
  window.addEventListener("popstate", sendLocation);
  if (document.readyState === "loading") {
    window.addEventListener("DOMContentLoaded", sendLocation, { once: true });
  } else {
    sendLocation();
  }
}());
