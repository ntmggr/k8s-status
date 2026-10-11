import { expect, test, type Locator, type Page } from '@playwright/test';
import { openStatusPage } from './status-page';

const SECTION_ORDER = ['HEALTH', 'HA', 'ISTIO', 'FLEET'];
const FIXTURE_RUNNING_PODS = '13';
const PIXEL_TOLERANCE = 1;

type Box = { x: number; y: number; width: number; height: number };

async function box(locator: Locator): Promise<Box> {
  const b = await locator.boundingBox();
  expect(b, 'element is not rendered').not.toBeNull();
  return b as Box;
}

// Elements that stick out past the right edge of the viewport, ignoring anything
// inside a container that scrolls or clips horizontally on purpose.
function overflowingElements(page: Page): Promise<string[]> {
  return page.evaluate((tolerance) => {
    const clipsX = (el: Element) => ['auto', 'scroll', 'hidden', 'clip'].includes(getComputedStyle(el).overflowX);
    const hasClippingAncestor = (el: Element) => {
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
        if (clipsX(p)) return true;
      }
      return false;
    };
    return [...document.body.querySelectorAll('*')]
      .filter((el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.right > window.innerWidth + tolerance && !hasClippingAncestor(el);
      })
      .map((el) => `${el.tagName.toLowerCase()}.${[...el.classList].join('.')}`);
  }, PIXEL_TOLERANCE);
}

test.beforeEach(async ({ page }) => {
  await openStatusPage(page);
});

test('page never scrolls sideways', async ({ page }) => {
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(page.viewportSize()!.width);
  expect(await overflowingElements(page)).toEqual([]);
});

test('left column holds Health, HA, Istio and Fleet in order', async ({ page }) => {
  const headings = await page.locator('.areas > .area > .cap-head').allInnerTexts();
  const names = headings.map((h) => {
    const text = h.trim().toUpperCase();
    return SECTION_ORDER.find((name) => text.startsWith(name)) ?? text;
  });
  expect(names).toEqual(SECTION_ORDER);
});

test('section content stays inside its section', async ({ page }) => {
  const escapes = await page.evaluate((tolerance) =>
    [...document.querySelectorAll('.areas > .area')].flatMap((area) => {
      const edge = area.getBoundingClientRect().right;
      return [...area.querySelectorAll('*')]
        .filter((el) => el.getBoundingClientRect().right > edge + tolerance)
        .map((el) => `${el.tagName.toLowerCase()}.${[...el.classList].join('.')}`);
    }),
  PIXEL_TOLERANCE);
  expect(escapes).toEqual([]);
});

test('sections are arranged for the viewport width', async ({ page }, testInfo) => {
  const rail = await box(page.locator('.split-main'));
  const side = await box(page.locator('.split-side'));
  const device = testInfo.project.name.split('-')[0];

  if (device === 'desktop') {
    expect(side.x).toBeGreaterThanOrEqual(rail.x + rail.width - PIXEL_TOLERANCE);
    expect(Math.abs(side.y - rail.y)).toBeLessThanOrEqual(PIXEL_TOLERANCE);
    return;
  }
  expect(side.y).toBeGreaterThanOrEqual(rail.y + rail.height - PIXEL_TOLERANCE);

  const [health, ha] = await Promise.all([
    box(page.locator('.areas > .area').nth(0)),
    box(page.locator('.areas > .area').nth(1)),
  ]);
  if (device === 'tablet') {
    expect(Math.abs(health.y - ha.y), 'Health and HA share a row').toBeLessThanOrEqual(PIXEL_TOLERANCE);
  } else {
    expect(ha.y, 'HA stacks under Health').toBeGreaterThanOrEqual(health.y + health.height - PIXEL_TOLERANCE);
  }
});

test('rings render real colours, never an escaped template value', async ({ page }) => {
  expect(await page.content()).not.toContain('ZgotmplZ');
});

test('Fleet counts only running pods', async ({ page }) => {
  const pods = page.locator('.areas > .area').nth(3).locator('.ringstat-num').first();
  await expect(pods).toHaveText(FIXTURE_RUNNING_PODS);
});
