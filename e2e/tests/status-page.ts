import { expect, type Locator, type Page } from '@playwright/test';

export const PAGE_PATH = '/k8s-status/?refresh=0';
const API_PATH = '/k8s-status/api/status';
const FIRST_FETCH_TIMEOUT_MS = 30_000;

// /healthz answers before the collector's first fetch completes, so wait for the
// API to report services before opening the page.
export async function openStatusPage(page: Page): Promise<void> {
  await expect
    .poll(
      async () => {
        const response = await page.request.get(API_PATH);
        if (!response.ok()) return 0;
        const body = (await response.json()) as { services?: unknown[] };
        return body.services?.length ?? 0;
      },
      { timeout: FIRST_FETCH_TIMEOUT_MS, message: 'k8s-status never reported any services' },
    )
    .toBeGreaterThan(0);

  await page.goto(PAGE_PATH);
  await page.evaluate(() => document.fonts.ready);
}

// Text derived from the wall clock ("checked 3s ago", "last deploy 50d ago").
export function clockDependent(page: Page): Locator[] {
  return [page.locator('header .meta'), page.locator('.sub')];
}
