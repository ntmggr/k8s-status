import { expect, test, type Locator, type Page } from '@playwright/test';
import { openStatusPage } from './status-page';

const SECTIONS = ['.kpis', '.insight-health', '.insight-resilience', '#cluster', '#attention'];
const FIXTURE_RUNNING_PODS = '13';
const PIXEL_TOLERANCE = 1;

type Box = { x: number; y: number; width: number; height: number };

async function box(locator: Locator): Promise<Box> {
  const b = await locator.boundingBox();
  expect(b, 'element is not rendered').not.toBeNull();
  return b as Box;
}

function sameRow(a: Box, b: Box): boolean {
  return Math.abs(a.y - b.y) <= PIXEL_TOLERANCE;
}

function below(lower: Box, upper: Box): boolean {
  return lower.y >= upper.y + upper.height - PIXEL_TOLERANCE;
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

test('summary, Service health, Resilience, Cluster and Attention appear in order', async ({ page }) => {
  const order = await page.evaluate((selectors) => {
    const found = selectors.map((s) => document.querySelector(s));
    if (found.some((el) => !el)) return selectors.filter((_, i) => !found[i]).map((s) => `missing ${s}`);
    return [...found]
      .sort((a, b) => (a!.compareDocumentPosition(b!) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1))
      .map((el) => selectors[found.indexOf(el)]);
  }, SECTIONS);
  expect(order).toEqual(SECTIONS);
});

test('section content stays inside its section', async ({ page }) => {
  const escapes = await page.evaluate((tolerance) =>
    [...document.querySelectorAll('.kpi, .insight, .split-side > *')].flatMap((section) => {
      const edge = section.getBoundingClientRect().right;
      return [...section.querySelectorAll('*')]
        .filter((el) => el.getBoundingClientRect().right > edge + tolerance && !el.closest('.node-table-scroll'))
        .map((el) => `${el.tagName.toLowerCase()}.${[...el.classList].join('.')}`);
    }),
  PIXEL_TOLERANCE);
  expect(escapes).toEqual([]);
});

test('sections are arranged for the viewport width', async ({ page }, testInfo) => {
  const [sidebar, content, health, resilience, cluster, attention] = await Promise.all(
    ['.dashboard-sidebar', '.dashboard-content', '.insight-health', '.insight-resilience', '#cluster', '#attention']
      .map((s) => box(page.locator(s))),
  );
  const device = testInfo.project.name.split('-')[0];

  if (device === 'phone') {
    expect(below(content, sidebar), 'sidebar sits above the content').toBe(true);
    expect(below(resilience, health), 'Resilience stacks under Service health').toBe(true);
    expect(below(attention, cluster), 'Attention stacks under Cluster').toBe(true);
    return;
  }
  expect(content.x, 'sidebar sits left of the content').toBeGreaterThanOrEqual(sidebar.x + sidebar.width - PIXEL_TOLERANCE);
  expect(sameRow(sidebar, content), 'sidebar and content start on the same row').toBe(true);
  expect(sameRow(cluster, attention), 'Cluster and Attention share a row').toBe(true);
  if (device === 'desktop') {
    expect(sameRow(health, resilience), 'Service health and Resilience share a row').toBe(true);
  } else {
    expect(below(resilience, health), 'Resilience stacks under Service health').toBe(true);
  }
});

test('gauges render real colours, never an escaped template value', async ({ page }) => {
  expect(await page.content()).not.toContain('ZgotmplZ');
});

test('Running pods card counts only running pods', async ({ page }) => {
  const card = page.locator('.kpi').filter({ has: page.locator('.kpi-lab', { hasText: 'Running pods' }) });
  await expect(card.locator('.kpi-val')).toHaveText(FIXTURE_RUNNING_PODS);
});
