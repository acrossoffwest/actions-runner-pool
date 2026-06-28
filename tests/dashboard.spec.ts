import { test, expect } from "@playwright/test";

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const STATS_EMPTY = {
  jobs: { pending: 0, dispatched: 0, in_progress: 0, completed: 0 },
  capacity: { active_runners: 0, available_slots: 4, max_concurrent_runners: 4 },
};

const STATS_BUSY = {
  jobs: { pending: 2, dispatched: 1, in_progress: 3, completed: 17 },
  capacity: { active_runners: 8, available_slots: 2, max_concurrent_runners: 10 },
};

const STATS_FULL = {
  jobs: { pending: 0, dispatched: 0, in_progress: 10, completed: 5 },
  capacity: { active_runners: 10, available_slots: 0, max_concurrent_runners: 10 },
};

const JOBS_EMPTY = { jobs: [] };

const JOBS_TWO = {
  jobs: [
    {
      id: 1,
      repo: "org/repo-a",
      job_name: "build",
      workflow_name: "CI",
      status: "in_progress",
      runner_name: "runner-abc",
      updated_at: new Date().toISOString(),
    },
    {
      id: 2,
      repo: "org/repo-b",
      job_name: "lint",
      workflow_name: "Lint",
      status: "pending",
      runner_name: "",
      updated_at: new Date().toISOString(),
    },
  ],
};

const JOBS_WITH_COMPLETED = {
  jobs: [
    {
      id: 1,
      repo: "org/repo-a",
      job_name: "build",
      workflow_name: "CI",
      status: "completed",
      conclusion: "success",
      runner_name: "runner-abc",
      updated_at: new Date().toISOString(),
    },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function mockApis(
  page: import("@playwright/test").Page,
  stats = STATS_EMPTY,
  jobs = JOBS_EMPTY
) {
  await page.route("**/stats", (r) =>
    r.fulfill({ contentType: "application/json", body: JSON.stringify(stats) })
  );
  await page.route("**/jobs**", (r) =>
    r.fulfill({ contentType: "application/json", body: JSON.stringify(jobs) })
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => sessionStorage.clear());
});

test("renders page shell with required elements", async ({ page }) => {
  await mockApis(page);
  await page.goto("/");

  await expect(page.locator("#app")).toBeVisible();
  // authForm lives inside admin drawer (always in DOM)
  await expect(page.locator("#authForm")).toBeAttached();
  await expect(page.locator(".stats")).toBeVisible();
  // DevServer has AllowAdminEdit=true → sr-only table with 6 th is rendered
  await expect(page.locator("thead tr th")).toHaveCount(6);
  // Capacity bar element is in DOM
  await expect(page.locator("#cap_bar")).toBeAttached();
});

test("stat tiles update from /stats response", async ({ page }) => {
  await mockApis(page, STATS_BUSY);
  await page.goto("/");

  await expect(page.locator("#s_in_progress")).toHaveText("3");
  await expect(page.locator("#s_pending")).toHaveText("2");
  await expect(page.locator("#s_dispatched")).toHaveText("1");
  await expect(page.locator("#s_completed")).toHaveText("17");
  await expect(page.locator("#cap_active")).toHaveText("8");
  await expect(page.locator("#cap_slots")).toHaveText("2");
});

test("renders job cards from /jobs response", async ({ page }) => {
  await mockApis(page, STATS_EMPTY, JOBS_TWO);
  await page.goto("/");

  const cards = page.locator(".board .card[data-card]");
  await expect(cards).toHaveCount(2);
  await expect(page.locator("#tableCount")).toHaveText("2 jobs");
  await expect(page.locator("#emptyJobs")).toBeHidden();

  // Both status badges present
  await expect(page.locator(".s-in_progress").first()).toBeVisible();
  await expect(page.locator(".s-pending").first()).toBeVisible();
});

test("empty state shown when /jobs returns no results", async ({ page }) => {
  await mockApis(page);
  await page.goto("/");

  await expect(page.locator("#emptyJobs")).toBeVisible();
  await expect(page.locator(".board .card[data-card]")).toHaveCount(0);
  await expect(page.locator("#tableCount")).toHaveText("0 jobs");
});

test("filtering by status hides non-matching cards", async ({ page }) => {
  await mockApis(page, STATS_EMPTY, JOBS_TWO);
  await page.goto("/");

  // Wait for both cards
  await expect(page.locator(".board .card[data-card]")).toHaveCount(2);

  // Filter to pending only — in_progress card disappears
  await page.locator('input[name=status][value=pending]').check();
  await expect(page.locator(".board .card[data-card]")).toHaveCount(1);
  await expect(page.locator(".board .card .s-pending")).toBeVisible();
  await expect(page.locator(".board .card .s-in_progress")).toHaveCount(0);
});

test("filtering by repo narrows visible cards", async ({ page }) => {
  await mockApis(page, STATS_EMPTY, JOBS_TWO);
  await page.goto("/");

  await expect(page.locator(".board .card[data-card]")).toHaveCount(2);

  // Type in repo filter — client-side, no new request needed
  await page.locator("#repoFilter").fill("repo-a");
  await page.locator("#repoFilter").dispatchEvent("input");
  await expect(page.locator(".board .card[data-card]")).toHaveCount(1);
});

test("admin drawer opens on Admin button click", async ({ page }) => {
  await mockApis(page);
  await page.goto("/");

  await expect(page.locator("#adminDrawer")).toHaveAttribute("aria-hidden", "true");
  await page.locator("#adminBtn").click();
  await expect(page.locator("#adminDrawer")).toHaveAttribute("aria-hidden", "false");
  await expect(page.locator("#authForm")).toBeVisible();
});

test("saves token to sessionStorage on form submit", async ({ page }) => {
  await mockApis(page);
  await page.goto("/");

  await page.locator("#adminBtn").click();
  await page.locator("#tokenInput").fill("my-secret-token");
  await page.locator("#saveTokenButton").click();

  const stored = await page.evaluate(() =>
    sessionStorage.getItem("gharp.adminToken")
  );
  expect(stored).toBe("my-secret-token");
});

test("clear token button removes token from sessionStorage", async ({ page }) => {
  await mockApis(page);
  await page.goto("/");

  await page.locator("#adminBtn").click();
  await page.locator("#tokenInput").fill("to-be-cleared");
  await page.locator("#saveTokenButton").click();
  await page.locator("#clearTokenButton").click();

  const stored = await page.evaluate(() =>
    sessionStorage.getItem("gharp.adminToken")
  );
  expect(stored).toBeNull();
});

test("retry button calls POST /jobs/:id/retry", async ({ page }) => {
  // Token required by mutate()
  await page.addInitScript(() =>
    sessionStorage.setItem("gharp.adminToken", "test-token")
  );
  await mockApis(page, STATS_EMPTY, JOBS_WITH_COMPLETED);

  let retryCalled = false;
  await page.route("**/jobs/1/retry", (r) => {
    retryCalled = true;
    r.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });

  await page.goto("/");
  await expect(page.locator(".board .card[data-card]")).not.toHaveCount(0);
  const retryReq = page.waitForRequest("**/jobs/1/retry");
  await page.locator('[data-act="retry"]').first().click();
  await retryReq;
  expect(retryCalled).toBe(true);
});

test("cancel button calls POST /jobs/:id/cancel", async ({ page }) => {
  await page.addInitScript(() =>
    sessionStorage.setItem("gharp.adminToken", "test-token")
  );
  await mockApis(page, STATS_EMPTY, JOBS_TWO);

  let cancelCalled = false;
  await page.route("**/jobs/2/cancel", (r) => {
    cancelCalled = true;
    r.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });

  await page.goto("/");
  // Job 2 is pending → Cancel button in its card
  await expect(page.locator(".card[data-card='2']")).toBeVisible();
  const cancelReq = page.waitForRequest("**/jobs/2/cancel");
  await page.locator(".card[data-card='2'] [data-act='cancel']").click();
  await cancelReq;
  expect(cancelCalled).toBe(true);
});

test("capacity bar shows warn state at 80%+", async ({ page }) => {
  await mockApis(page, STATS_BUSY); // 8/10 = 80%
  await page.goto("/");
  await expect(page.locator("#cap_bar")).toHaveAttribute("data-state", "warn");
});

test("capacity bar shows full state at 100%", async ({ page }) => {
  await mockApis(page, STATS_FULL); // 10/10 = 100%
  await page.goto("/");
  await expect(page.locator("#cap_bar")).toHaveAttribute("data-state", "full");
});

test("401 from /stats opens admin drawer", async ({ page }) => {
  await page.route("**/stats", (r) =>
    r.fulfill({ status: 401, body: "Unauthorized" })
  );
  await page.route("**/jobs**", (r) =>
    r.fulfill({ status: 401, body: "Unauthorized" })
  );

  await page.goto("/");
  await expect(page.locator("#adminDrawer")).toHaveAttribute("aria-hidden", "false");
});

test("error banner visible on API 5xx failure", async ({ page }) => {
  await page.route("**/stats", (r) =>
    r.fulfill({ status: 500, body: "internal error" })
  );
  await page.route("**/jobs**", (r) =>
    r.fulfill({ contentType: "application/json", body: JSON.stringify(JOBS_EMPTY) })
  );

  await page.goto("/");
  await expect(page.locator("#errorBanner")).toBeVisible();
});

test("live indicator text updates after successful poll", async ({ page }) => {
  await mockApis(page);
  await page.goto("/");
  await expect(page.locator("#liveText")).not.toHaveText("connecting…");
});
