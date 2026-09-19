import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";

// The component tier runs against happy-dom with the API surface mocked at
// the client module; layout, real scrolling, viewports, and WebSockets stay
// in the Playwright suites.
export default defineConfig({
  plugins: [vue()],
  test: {
    environment: "happy-dom",
    include: ["src/**/*.spec.ts"],
  },
});
