import { expect, test } from "@playwright/test";
import {
  awaitWorkflowState,
  igniteDocumentationPractice,
  autoscaleAnchor,
  autoscalePage,
  ingressPage,
  ingressTerminologyAnchor,
  loginBootstrapAdmin,
  loginThroughConsole,
  postgres,
  readBootstrapAdmin,
  scaleRuntimeWorker,
} from "../support/admin";
import { createBatch, getBatch } from "../support/batch";

// The batch rollout proves the full declarative flow. The skip precondition
// is self-supplied: the autoscale page is published through the ordinary
// ignition first, then one batch over [autoscale, ingress] skips the
// published anchor and lets the scheduler drive the fresh page end to end.
// A re-run against a target where ingress is already published can only
// re-verify the skip semantics; the scheduler-chain assertions are gated.
test("documentation batches schedule, publish, skip, and roll up the corpus", async ({ page, request }) => {
  test.setTimeout(50 * 60_000);
  const token = await loginBootstrapAdmin(request);

  await scaleRuntimeWorker(1);
  const prestart = await igniteDocumentationPractice(request, token, autoscalePage, autoscaleAnchor);
  expect(prestart.status(), await prestart.text()).toBe(202);
  const prestarted = await prestart.json() as { workflow_id: string };
  await awaitWorkflowState(
    () => postgres(`SELECT state FROM document_workflows WHERE id = '${prestarted.workflow_id}'`),
    /(Published)/, 30 * 60_000);

  const ingressPublished = await postgres(`SELECT COUNT(*) FROM document_workflows WHERE page_path = '${ingressPage}' AND anchor = '${ingressTerminologyAnchor}' AND state = 'Published'`);
  const freshTarget = Number(ingressPublished) === 0;

  const batch = await createBatch(request, token, { kind: "pages", pages: [autoscalePage, ingressPage] });
  expect(batch.total_items).toBe(2);
  expect(["Pending", "Running"]).toContain(batch.state);

  await expect.poll(async () => {
    const current = await getBatch(request, token, batch.id);
    return `${current.state}:${current.counts?.Failed ?? 0}`;
  }, { timeout: 35 * 60_000, intervals: [3_000, 5_000, 10_000] }).toBe("Completed:0");
  const finished = await getBatch(request, token, batch.id);
  expect(finished.counts?.Skipped ?? 0).toBe(freshTarget ? 1 : 2);
  expect(finished.counts?.Published ?? 0).toBe(freshTarget ? 1 : 0);

  if (freshTarget) {
    // The scheduler drove the practice through the deterministic level-2
    // anchor rule without any human ignition.
    const ingressWorkflow = await postgres(`SELECT id FROM document_workflows WHERE page_path = '${ingressPage}' AND anchor = '${ingressTerminologyAnchor}' AND state = 'Published'`);
    expect(ingressWorkflow).toMatch(/^document-workflow-/);
  }

  // The console corpus view shows the rollup row server-side. The expected
  // published count comes from the database: the controls scenario may have
  // published a second anchor of the autoscale page before this file ran.
  await loginThroughConsole(page, readBootstrapAdmin());
  await page.getByRole("button", { name: "文档" }).click();
  await expect(page.getByRole("heading", { name: "文档语料与批次" })).toBeVisible();
  // The sub-view defaults to the corpus tree, but the batch auto-open in the
  // composable can leave the batches pane rendered; pin the tree explicitly.
  await page.getByRole("button", { name: "语料树" }).click();
  const autoscaleRow = page.locator(".admin-corpus-page-toggle", { hasText: "Horizontal Pod Autoscaling" });
  await expect(autoscaleRow).toBeVisible({ timeout: 15_000 });
  const autoscalePublished = await postgres(`SELECT COUNT(*) FROM document_workflows WHERE page_path = '${autoscalePage}' AND state = 'Published'`);
  await expect(autoscaleRow).toContainText(`已发布 ${autoscalePublished}/`);

  // The batch detail shows per-item rows with their terminal states. The
  // list is newest-first and row text carries only the shortened id, so the
  // just-created batch is the first row.
  await page.getByRole("button", { name: "批次" }).click();
  const batchRow = page.locator(".admin-batch-row").first();
  await expect(batchRow).toBeVisible();
  await expect(batchRow).toContainText("Completed");
  await batchRow.locator(".admin-batch-toggle").click();
  await expect(page.locator(".admin-batch-detail")).toContainText("Skipped");
  if (freshTarget) {
    await expect(page.locator(".admin-batch-detail")).toContainText("Published");
  }

  // Failures-only filtering hides healthy pages. The filters apply on form
  // submit, so the checkbox needs its own pass.
  await page.getByRole("button", { name: "语料树" }).click();
  await page.getByLabel("Search page titles").fill("Horizontal Pod Autoscaling");
  await page.getByRole("button", { name: "应用" }).click();
  await page.getByLabel("只看失败/卡住").check();
  await page.getByRole("button", { name: "应用" }).click();
  await expect(page.locator(".admin-corpus-page-toggle")).toHaveCount(0);
});
