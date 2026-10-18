import { expect, type Page } from '@playwright/test';

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

// The server renders these from the wall clock, so two runs seconds apart differ.
// Masking them is not enough: the text's width changes with its digit count ("9s"
// vs "10s"), which moves the mask's edge and can rewrap the header on a phone.
const CLOCK_TEXT: Array<[RegExp, string]> = [
  [/checked \d+s ago/, 'checked 0s ago'],
  [/data is \d+s stale/, 'data is 0s stale'],
  [/last deploy (just now|\d+[mhd] ago)/, 'last deploy 0d ago'],
];

// Rewrites clock-derived text to fixed values and returns how many text nodes
// changed. Zero means the formats above no longer match what the page renders.
export async function freezeClockText(page: Page): Promise<number> {
  return page.evaluate((rules) => {
    let changed = 0;
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const before = node.nodeValue ?? '';
      const after = rules.reduce((text, [source, to]) => text.replace(new RegExp(source), to), before);
      if (after !== before) {
        node.nodeValue = after;
        changed++;
      }
    }
    return changed;
  }, CLOCK_TEXT.map(([re, to]) => [re.source, to] as [string, string]));
}
