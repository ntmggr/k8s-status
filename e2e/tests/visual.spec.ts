import { expect, test } from '@playwright/test';
import { clockDependent, openStatusPage } from './status-page';

test('status page matches the baseline rendering', async ({ page }) => {
  await openStatusPage(page);
  await expect(page).toHaveScreenshot('status-page.png', {
    fullPage: true,
    mask: clockDependent(page),
  });
});
