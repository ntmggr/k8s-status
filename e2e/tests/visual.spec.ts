import { expect, test } from '@playwright/test';
import { freezeClockText, openStatusPage } from './status-page';

test('status page matches the baseline rendering', async ({ page }) => {
  await openStatusPage(page);
  expect(await freezeClockText(page), 'no clock-derived text found; update CLOCK_TEXT').toBeGreaterThan(0);
  await expect(page).toHaveScreenshot('status-page.png', { fullPage: true });
});
