import { expect, type APIRequestContext } from "@playwright/test";
import { apiBase } from "./admin";

export type Batch = {
  id: string;
  state: string;
  concurrency: number;
  total_items: number;
  counts?: Record<string, number>;
};

export type BatchItem = { id: string; page_path: string; anchor: string; state: string; detail?: string };

export async function createBatch(request: APIRequestContext, token: string, scope: Record<string, unknown>) {
  const created = await request.post(`${apiBase}/api/admin/documentation/batches`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { scope },
  });
  expect(created.status(), await created.text()).toBe(202);
  return (await created.json()) as Batch;
}

export async function getBatch(request: APIRequestContext, token: string, id: string): Promise<Batch> {
  const detail = await request.get(`${apiBase}/api/admin/documentation/batches/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(detail.status(), await detail.text()).toBe(200);
  return (await detail.json()) as Batch;
}

export async function listBatchItems(request: APIRequestContext, token: string, id: string) {
  const items = await request.get(`${apiBase}/api/admin/documentation/batches/${id}/items?limit=50`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(items.status(), await items.text()).toBe(200);
  return (await items.json()) as { items: BatchItem[] };
}

export async function findBatchItem(request: APIRequestContext, token: string, id: string, pagePath: string) {
  const page = await listBatchItems(request, token, id);
  return page.items.find((entry) => entry.page_path === pagePath);
}

export const terminalItemStates = ["Published", "NoPractice", "Rejected", "Failed", "Skipped", "Cancelled"];
