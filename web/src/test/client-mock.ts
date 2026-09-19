import { vi } from "vitest";
import type { api } from "../api/client";

// The component tier mocks at the client module boundary - the module that
// wraps the generated API types - instead of standing up a network stack.
// Specs call this from their vi.mock factory (the helper must be imported
// inside the factory because the call is hoisted above top-level imports):
//
//   vi.mock("../../api/client", async (importOriginal) => {
//     const { automockApi } = await import("../../test/client-mock");
//     const actual = await importOriginal<typeof import("../../api/client")>();
//     return { ...actual, api: automockApi(actual.api) };
//   });
//
// Every endpoint becomes a fresh vi.fn() while the module's auth/token
// helpers keep their real implementations (they only touch localStorage).
export function automockApi(actual: typeof api): typeof api {
	const mock: Record<string, unknown> = {};
	for (const key of Object.keys(actual)) mock[key] = vi.fn();
	return mock as unknown as typeof api;
}
