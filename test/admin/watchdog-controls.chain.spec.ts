import { expect, test, type APIRequestContext } from "@playwright/test";
import {
  apiBase,
  awaitWorkflowState,
  igniteDocumentationPractice,
  autoscalePage,
  ingressPage,
  ingressWhatIsAnchor,
  loginBootstrapAdmin,
  postgres,
  scaleRuntimeWorker,
} from "../support/admin";
import { createBatch, findBatchItem } from "../support/batch";

// One shared park drives the scenarios that used to be two chains: the
// watchdog mapping a SQL-failed runnable action onto its workflow, and the
// batch controls operating on the resulting Failed and in-flight items. The
// verbs' state-machine semantics live in the handler/application tiers; this
// chain keeps them thin (status + audit counts) and spends its time on the
// cross-system wiring: ignition -> queue -> watchdog -> batch scheduler ->
// retry -> worker reclaim -> publication.
const companionAnchor = "algorithm-details";

const mergedScope = {
  kind: "pages",
  pages: [ingressPage, autoscalePage],
  overrides: [
    { page_path: ingressPage, anchor: ingressWhatIsAnchor },
    { page_path: autoscalePage, anchor: companionAnchor },
  ],
};

async function parkAtMaterializingArtifact(request: APIRequestContext, token: string, pagePath: string, anchor: string) {
  const start = await igniteDocumentationPractice(request, token, pagePath, anchor);
  expect(start.status(), await start.text()).toBe(202);
  const started = await start.json() as { workflow_id: string };
  await awaitWorkflowState(
    () => postgres(`SELECT state FROM document_workflows WHERE id = '${started.workflow_id}'`),
    /(MaterializingArtifact)/, 30 * 60_000);
  return started.workflow_id;
}

// Simulate the worker-side failure the watchdog watches for.
async function failQueuedAction(workflowId: string, failureCode: string) {
  const actionKey = await postgres(`SELECT a.action_key FROM runnable_actions a JOIN document_runnable_actions b ON b.action_key = a.action_key WHERE b.workflow_id = '${workflowId}' AND a.state = 'queued' LIMIT 1`);
  expect(actionKey).not.toBe("");
  await postgres(`UPDATE runnable_actions SET state = 'failed', failure_class = 'artifact', failure_code = '${failureCode}', failure_summary = 'simulated worker failure' WHERE action_key = '${actionKey}'`);
}

test("watchdog and batch controls share one parked chain", async ({ request }) => {
  test.setTimeout(50 * 60_000);
  const token = await loginBootstrapAdmin(request);

  // Park the whole chain by removing the worker, then let the watchdog map
  // the SQL-failed action onto the workflow: a system ledger entry and no
  // human audit.
  await scaleRuntimeWorker(0);
  const failedWorkflowId = await parkAtMaterializingArtifact(request, token, ingressPage, ingressWhatIsAnchor);
  await failQueuedAction(failedWorkflowId, "watchdog-e2e");
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${failedWorkflowId}'`), {
    timeout: 2 * 60_000,
    intervals: [1_000, 2_000, 5_000],
  }).toBe("Failed");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${failedWorkflowId}' AND kind = 'watchdog.force_fail' AND owner_role = 'system'`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.workflow.force_fail' AND target_id = '${failedWorkflowId}'`)).toBe("0");

  // The companion parks in-flight; its item keeps the shared batch alive.
  await parkAtMaterializingArtifact(request, token, autoscalePage, companionAnchor);

  // The batch items follow their workflows: Failed maps straight onto the
  // failed page while the parked companion stays Running.
  const batch = await createBatch(request, token, mergedScope);
  await expect.poll(async () => (await findBatchItem(request, token, batch.id, ingressPage))?.state ?? "missing", {
    timeout: 3 * 60_000,
    intervals: [2_000, 5_000],
  }).toBe("Failed");
  await expect.poll(async () => (await findBatchItem(request, token, batch.id, autoscalePage))?.state ?? "missing", {
    timeout: 3 * 60_000,
    intervals: [2_000, 5_000],
  }).toBe("Running");

  // Thin control verification: every verb answers 200 with its batch state
  // and exactly one human audit. What the verbs mean for in-flight items is
  // asserted in the handler and application tiers.
  const paused = await request.post(`${apiBase}/api/admin/documentation/batches/${batch.id}/pause`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: pause on the merged chain" },
  });
  expect(paused.status(), await paused.text()).toBe(200);
  expect(((await paused.json()) as { state: string }).state).toBe("Paused");
  const resumed = await request.post(`${apiBase}/api/admin/documentation/batches/${batch.id}/resume`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: resume on the merged chain" },
  });
  expect(resumed.status(), await resumed.text()).toBe(200);
  expect(((await resumed.json()) as { state: string }).state).toBe("Running");
  const cancelled = await request.post(`${apiBase}/api/admin/documentation/batches/${batch.id}/cancel`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: cancel leaves in-flight work alone" },
  });
  expect(cancelled.status(), await cancelled.text()).toBe(200);
  expect(((await cancelled.json()) as { state: string }).state).toBe("Cancelled");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.batch.pause' AND target_id = '${batch.id}'`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.batch.resume' AND target_id = '${batch.id}'`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.batch.cancel' AND target_id = '${batch.id}'`)).toBe("1");

  // A second batch over the same scope remaps the Failed item and keeps the
  // companion item alive - the live batch state retry-failed requires.
  const retriedBatch = await createBatch(request, token, mergedScope);
  await expect.poll(async () => (await findBatchItem(request, token, retriedBatch.id, ingressPage))?.state ?? "missing", {
    timeout: 3 * 60_000,
    intervals: [2_000, 5_000],
  }).toBe("Failed");
  await expect.poll(async () => (await findBatchItem(request, token, retriedBatch.id, autoscalePage))?.state ?? "missing", {
    timeout: 3 * 60_000,
    intervals: [2_000, 5_000],
  }).toBe("Running");

  const retried = await request.post(`${apiBase}/api/admin/documentation/batches/${retriedBatch.id}/retry-failed`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reason: "E2E: retry the failed page on the merged chain" },
  });
  expect(retried.status(), await retried.text()).toBe(200);
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.batch.retry' AND target_id = '${retriedBatch.id}'`)).toBe("1");

  // The worker return drives the re-enqueued page through publication.
  await scaleRuntimeWorker(1);
  await expect.poll(async () => (await findBatchItem(request, token, retriedBatch.id, ingressPage))?.state ?? "missing", {
    timeout: 30 * 60_000,
    intervals: [3_000, 5_000, 10_000],
  }).toBe("Published");
  expect(await postgres(`SELECT state FROM document_workflows WHERE id = '${failedWorkflowId}'`)).toBe("Published");
});
