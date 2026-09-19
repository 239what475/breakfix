import { expect, test } from "@playwright/test";
import {
  apiBase,
  loginBootstrapAdmin,
  loginThroughConsole,
  podLifetimeAnchor,
  podLifecyclePage,
  postgres,
  readBootstrapAdmin,
  scaleRuntimeWorker,
} from "../support/admin";

const practiceBody = { page_path: podLifecyclePage, anchor: podLifetimeAnchor };

test("admin console rescues a stuck documentation workflow end to end", async ({ page, request }) => {
  test.setTimeout(15 * 60_000);
  const session = readBootstrapAdmin();
  await loginThroughConsole(page, session);
  // The console shell mirrors the app layout: sidebar identity + queue stat
  // cells + icon tabs, with the active section heading in the content column.
  await expect(page.getByLabel("Runnable action queue")).toBeVisible();
  await expect(page.getByRole("heading", { name: "工作流观测与解卡" })).toBeVisible();
  const adminToken = await loginBootstrapAdmin(request);

  // Removing the worker first simulates a permanently stalled provider: the
  // Agent phases run against the in-cluster fixture, then the workflow parks
  // in MaterializingArtifact with a queued action and no Worker to claim it.
  await scaleRuntimeWorker(0);
  const start = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${adminToken}` }, data: practiceBody });
  expect(start.status(), await start.text()).toBe(202);
  const started = await start.json() as { workflow_id: string };
  const workflowId = started.workflow_id;
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`), {
    timeout: 90_000,
    intervals: [1_000, 2_000],
  }).toBe("MaterializingArtifact");

  // The workflow list shows the stalled workflow on the stage stepper; the
  // overflow menu is the only entry to the destructive verbs. Force-fail
  // needs a reason and runs through the console dialog with its audit summary.
  await page.getByRole("button", { name: "Refresh", exact: true }).first().click();
  const workflowRow = page.locator(".admin-workflow-row", { hasText: workflowId });
  await expect(workflowRow).toBeVisible();
  await expect(workflowRow).toContainText("MaterializingArtifact");
  await expect(workflowRow.locator(".workflow-stepper .stepper-node.current")).toContainText("物化");
  await workflowRow.getByRole("button", { name: "工作流操作" }).click();
  await workflowRow.getByRole("menuitem", { name: "Force-fail 强制失败" }).click();
  // The confirm button stays disabled until the required reason is typed;
  // the API's own reason validation is covered by the handler tests.
  await page.getByLabel(/Reason/).fill("E2E: worker removed, materialization stalled");
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(workflowRow).toContainText("Failed");
  // Terminal cards carry no stepper; the failure is badge + meta only.
  await expect(workflowRow.locator(".workflow-stepper")).toHaveCount(0);

  expect(await postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`)).toBe("Failed");
  expect(await postgres(`SELECT COUNT(*) FROM document_artifact_ledger WHERE workflow_id = '${workflowId}' AND kind = 'admin.force_fail' AND owner_role = 'admin'`)).toBe("1");
  expect(await postgres(`SELECT COUNT(*) FROM human_action_audits WHERE action = 'documentation.workflow.force_fail' AND target_id = '${workflowId}'`)).toBe("1");
  const repeatForceFail = await request.post(`${apiBase}/api/admin/documentation/workflows/${workflowId}/force-fail`, {
    headers: { Authorization: `Bearer ${adminToken}` },
    data: { reason: "again" },
  });
  expect(repeatForceFail.status()).toBe(409);

  // Restart resets the failed workflow to Planning; a second restart on the
  // non-terminal Planning state conflicts.
  await workflowRow.getByRole("button", { name: "工作流操作" }).click();
  await workflowRow.getByRole("menuitem", { name: "Restart 重启" }).click();
  await page.getByLabel(/Reason/).fill("E2E: retry after worker recovery");
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(workflowRow).toContainText("Planning");
  await expect(workflowRow.locator(".workflow-stepper .stepper-node.current")).toContainText("规划");
  expect(await postgres(`SELECT revision FROM document_workflows WHERE id = '${workflowId}'`)).toBe("2");
  const repeatRestart = await request.post(`${apiBase}/api/admin/documentation/workflows/${workflowId}/restart`, {
    headers: { Authorization: `Bearer ${adminToken}` },
    data: { reason: "again" },
  });
  expect(repeatRestart.status()).toBe(409);

  // The ordinary ignition endpoint re-drives the restarted workflow. The
  // Worker comes back only after the re-run has parked again, so the console
  // actions are what rescue the workflow, then publication completes.
  const reignite = await request.post(`${apiBase}/api/documentation/practice`, { headers: { Authorization: `Bearer ${adminToken}` }, data: practiceBody });
  expect(reignite.status(), await reignite.text()).toBe(202);
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`), {
    timeout: 90_000,
    intervals: [1_000, 2_000],
  }).toBe("MaterializingArtifact");
  await scaleRuntimeWorker(1);
  await expect.poll(async () => postgres(`SELECT state FROM document_workflows WHERE id = '${workflowId}'`), {
    timeout: 10 * 60_000,
    intervals: [2_000, 5_000, 10_000],
  }).toBe("Published");

  // The audit ledger lists every verb with its actor and transitions.
  const audit = await request.get(`${apiBase}/api/admin/audit?limit=50`, { headers: { Authorization: `Bearer ${adminToken}` } });
  expect(audit.status()).toBe(200);
  const auditPage = await audit.json() as { items: Array<{ action: string; detail: Record<string, string> }> };
  const auditActions = auditPage.items.map((item) => item.action);
  expect(auditActions).toContain("documentation.practice.start");
  expect(auditActions).toContain("documentation.workflow.force_fail");
  expect(auditActions).toContain("documentation.workflow.restart");
  const forceFailAudit = auditPage.items.find((item) => item.action === "documentation.workflow.force_fail");
  expect(forceFailAudit?.detail["from_state"]).toBe("MaterializingArtifact");
  expect(forceFailAudit?.detail["to_state"]).toBe("Failed");

  // The queue endpoint answers with the whole-queue summary.
  const queue = await request.get(`${apiBase}/api/admin/runnable-actions`, { headers: { Authorization: `Bearer ${adminToken}` } });
  expect(queue.status()).toBe(200);
  const queuePage = await queue.json() as { summary: { by_state: Record<string, number> }; items: Array<{ flag: string }> };
  expect(queuePage.summary.by_state).toHaveProperty("completed");

  // The environment observation endpoint answers against the live cluster.
  // By publication time the verification environment has already been
  // destroyed through the ordinary drain path, so an empty list is expected.
  const environments = await request.get(`${apiBase}/api/admin/environments`, { headers: { Authorization: `Bearer ${adminToken}` } });
  expect(environments.status()).toBe(200);
  const environmentList = await environments.json() as { environments: Array<{ name: string; phase: string }> };
  expect(environmentList.environments.every((entry) => entry.phase !== "Released")).toBe(true);
});
