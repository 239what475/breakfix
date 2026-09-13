import http from "node:http";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const files = {
  "/docs/": "index.html",
  "/docs/index.html": "index.html",
  "/docs/page/": "page.html",
  "/docs/page/index.html": "page.html",
  "/docs/context.js": "context.js",
  "/docs/sender/": "sender.html",
};

const server = http.createServer(async (request, response) => {
  const file = files[request.url?.split("#", 1)[0] || ""];
  if (!file) {
    response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
    response.end("not found");
    return;
  }
  try {
    const body = await readFile(join(root, file));
    response.writeHead(200, {
      "content-type": file.endsWith(".js") ? "text/javascript; charset=utf-8" : "text/html; charset=utf-8",
      "cache-control": "no-store",
    });
    response.end(body);
  } catch {
    response.writeHead(500);
    response.end("fixture error");
  }
});

server.listen(Number(process.env.DOCUMENTATION_FIXTURE_PORT || 1314), "127.0.0.1");
