import { expect, test, type APIRequestContext } from "@playwright/test";
import {
  apiBase,
  autoscalePage,
  ingressPage,
  ingressPrerequisitesAnchor,
  ingressWhatIsAnchor,
  loginBootstrapAdmin,
  postgres,
  scaleRuntimeWorker,
} from "../support/admin";
import { createBatch, findBatchItem } from "../support/batch";

// Both scenarios run on explicitly overridden anchors of the ingress page, so
// they neither collide with each other nor with the rollout scenario's
// terminology anchor, and they exercise the (page, anchor) scope override.
const whatIsScope = { kind: "pages", pages: [ingressPage], overrides: [{ page_path: ingressPage, anchor: ingressWhatIsAnchor }] };
const prerequisitesScope = { kind: "pages", pages: [ingressPage], overrides: [{ page_path: ingressPage, anchor: ingressPrerequisitesAnchor }] };

// Park a fresh practice at MaterializingArtifact (worker removed) and let the
// watchdog map the SQL-failed action onto the workflow as Failed.
async function failParkedWorkflow(request: APIRequestContext, token: string, anchor: string) {
  await scaleRuntimeWorker(0);
  const start = await request.post(`${apiBase}/api/documentation/practice`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { page_path: ingressPage, anchor },
  });
  expect(start.status(), await start.text()).toBe(202);
  const started = await start.json() as { workflow_id: string };
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`), {
    timeout: 3 * 60_000,
    intervals: [1_000, 2_000],
  }).toBe("MaterializingArtifact");

  // Simulate the worker-side failure the watchdog watches for.
  const actionKey = await postgres(`SELECT a.action_key FROM runnable_actions a JOIN document_runnable_actions b ON b.action_key = a.action_key WHERE b.workflow_id = '${started.workflow_id}' AND a.state = 'queued' LIMIT 1`);
  expect(actionKey).not.toBe("");
  await postgres(`UPDATE runnable_actions SET state = 'failed', failure_class = 'artifact', failure_code = 'watchdog-e2e', failure_summary = 'simulated worker failure' WHERE action_key = '${actionKey}'`);

  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`), {
    timeout: 2 * 60_000,
    intervals: [1_000, 2_000, 5_000],
  }).toBe("Failed");
  return started.workflow_id;
}

// The watchdog replaces the human force-fail escape hatch: when the bound
// runnable action fails, the workflow maps onto Failed with a system ledger
// entry and the batch item follows - no administrator involved.
test("watchdog maps a failed runnable action onto its workflow and batch item", async ({ request }) => {
  test.setTimeout(10 * 60_000);
  const token = await loginBootstrapAdmin(request);

  const workflowId = await failParkedWorkflow(request, token, ingressWhatIsAnchor);

  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${workflowId}' AND kind = 'watchdog.force_fail' AND owner_role = 'system'`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.workflow.force_fail' AND target_id = '${workflowId}'`)).toBe("0");

  // The batch item follows its workflow to Failed.
  const batch = await createBatch(request, token, whatIsScope);
  await expect.poll(async () => {
    const item = await findBatchItem(request, token, batch.id, ingressPage);
    return item?.state ?? "missing";
  }, { timeout: 3 * 60_000, intervals: [2_000, 5_000] }).toBe("Failed");
});

// Pause stops new ignitions, resume continues, and cancel leaves in-flight
// items to their own terminal outcome (here a genuine watchdog failure, which
// proves cancel neither force-fails nor abandons the item); retry re-enqueues
// the failed page and the scheduler drives it to publication.
test("batch pause, resume, cancel, and retry keep item semantics", async ({ request }) => {
  test.setTimeout(20 * 60_000);
  const token = await loginBootstrapAdmin(request);

  // A failed workflow on the prerequisites anchor is this scenario's own
  // subject; restart it so a fresh batch chain can run.
  const failedWorkflow = await failParkedWorkflow(request, token, ingressPrerequisitesAnchor);
  const restart = await request.post(`${apiBase}/api/admin/documentation/workflows/${failedWorkflow}/restart`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: batch retry cycle" },
  });
  expect(restart.status(), await restart.text()).toBe(200);

  // With the worker removed the chain parks at MaterializingArtifact and the
  // item stays Running - the perfect in-flight subject for the controls.
  const batch = await createBatch(request, token, prerequisitesScope);
  await expect.poll(async () => {
    const item = await findBatchItem(request, token, batch.id, ingressPage);
    return item?.state ?? "missing";
  }, { timeout: 3 * 60_000, intervals: [2_000, 5_000] }).toBe("Running");

  const paused = await request.post(`${apiBase}/api/admin/documentation/batches/${batch.id}/pause`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: pause before resume" },
  });
  expect(paused.status(), await paused.text()).toBe(200);
  expect(((await paused.json()) as { state: string }).state).toBe("Paused");

  const resumed = await request.post(`${apiBase}/api/admin/documentation/batches/${batch.id}/resume`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: resume after pause" },
  });
  expect(resumed.status(), await resumed.text()).toBe(200);
  expect(((await resumed.json()) as { state: string }).state).toBe("Running");

  const cancelled = await request.post(`${apiBase}/api/admin/documentation/batches/${batch.id}/cancel`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: cancel leaves in-flight work alone" },
  });
  expect(cancelled.status(), await cancelled.text()).toBe(200);
  expect(((await cancelled.json()) as { state: string }).state).toBe("Cancelled");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.batch.cancel' AND target_id = '${batch.id}'`)).toBe("1");

  // The cancelled batch's in-flight item still reaches its own outcome: the
  // restarted attempt is failed through the watchdog, not by the cancel.
  const actionKey = await postgres(`SELECT a.action_key FROM runnable_actions a JOIN document_runnable_actions b ON b.action_key = a.action_key WHERE b.workflow_id = '${failedWorkflow}' AND a.state = 'queued' LIMIT 1`);
  await postgres(`UPDATE runnable_actions SET state = 'failed', failure_class = 'artifact', failure_code = 'cancel-e2e', failure_summary = 'simulated worker failure' WHERE action_key = '${actionKey}'`);
  await expect.poll(async () => {
    const item = await findBatchItem(request, token, batch.id, ingressPage);
    return item?.state ?? "missing";
  }, { timeout: 3 * 60_000, intervals: [2_000, 5_000] }).toBe("Failed");

  // Retry re-enqueues the failed page on a live batch. A single-item batch
  // auto-completes once its item fails, and retry requires a Running or
  // Paused batch - so a second, deliberately parked item keeps the batch
  // alive: the level-3 algorithm-details anchor of the autoscale page.
  const companionAnchor = "algorithm-details";
  const companionStart = await request.post(`${apiBase}/api/documentation/practice`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { page_path: autoscalePage, anchor: companionAnchor },
  });
  expect(companionStart.status(), await companionStart.text()).toBe(202);
  const companion = await companionStart.json() as { workflow_id: string };
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${companion.workflow_id}'`), {
    timeout: 3 * 60_000,
    intervals: [1_000, 2_000],
  }).toBe("MaterializingArtifact");

  const retriedBatch = await createBatch(request, token, {
    kind: "pages",
    pages: [ingressPage, autoscalePage],
    overrides: [
      { page_path: ingressPage, anchor: ingressPrerequisitesAnchor },
      { page_path: autoscalePage, anchor: companionAnchor },
    ],
  });
  // The ingress item maps straight onto its Failed workflow; the companion's
  // parked workflow keeps its item Running, so the batch itself stays Running
  // - exactly the live state the retry verb requires.
  await expect.poll(async () => {
    const item = await findBatchItem(request, token, retriedBatch.id, ingressPage);
    return item?.state ?? "missing";
  }, { timeout: 3 * 60_000, intervals: [2_000, 5_000] }).toBe("Failed");

  const retried = await request.post(`${apiBase}/api/admin/documentation/batches/${retriedBatch.id}/retry-failed`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: retry the failed page" },
  });
  expect(retried.status(), await retried.text()).toBe(200);
  await scaleRuntimeWorker(1);
  await expect.poll(async () => {
    const item = await findBatchItem(request, token, retriedBatch.id, ingressPage);
    return item?.state ?? "missing";
  }, { timeout: 12 * 60_000, intervals: [3_000, 5_000, 10_000] }).toBe("Published");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.batch.retry' AND target_id = '${retriedBatch.id}'`)).toBe("1");
  expect(await postgres(`SELECT state FROM document_workflows WHERE id = '${failedWorkflow}'`)).toBe("Published");
});
