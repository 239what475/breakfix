import { expect, test } from "@playwright/test";
import {
  loginBootstrapAdmin,
  loginThroughConsole,
  postgres,
  readBootstrapAdmin,
  resolveUserId,
} from "../support/admin";

// The audit console renders whatever the ledger holds; a seeded force-fail
// row exercises the rendering without waiting for a real workflow chain.
// The real action -> audit -> API path is asserted in the rescue scenario.
test("the audit ledger renders rows and expands their payloads", async ({ page, request }) => {
  const token = await loginBootstrapAdmin(request);
  const adminUserId = await resolveUserId(request, token, readBootstrapAdmin().username);
  await postgres(`INSERT INTO human_action_audits (id, user_id, action, target_type, target_id, detail, created_at) VALUES ('audit-ui-e2e-${Date.now()}', '${adminUserId}', 'documentation.workflow.force_fail', 'document_workflow', 'document-workflow-audit-ui', '{"from_state":"MaterializingArtifact","to_state":"Failed","reason":"seeded for the audit console rendering"}', now())`);

  await loginThroughConsole(page, readBootstrapAdmin());
  await page.getByRole("button", { name: "审计" }).click();
  const forceFailRow = page.locator(".admin-audit-toggle", { hasText: "documentation.workflow.force_fail" });
  await expect(forceFailRow).toBeVisible();
  await forceFailRow.click();
  const expandedDetail = page.locator(".admin-audit-item", { hasText: "documentation.workflow.force_fail" }).locator(".admin-audit-detail");
  await expect(expandedDetail).toContainText("from_state");
  await expect(expandedDetail).toContainText("MaterializingArtifact");
});
