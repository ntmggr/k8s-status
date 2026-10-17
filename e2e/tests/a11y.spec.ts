import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { openStatusPage } from './status-page';

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];
const BLOCKING_IMPACTS = new Set(['serious', 'critical']);

// Known failures, each to be removed by the change that fixes it.
const KNOWN_ISSUES: string[] = [];

test('no serious or critical WCAG 2.1 AA violations', async ({ page }) => {
  await openStatusPage(page);
  const results = await new AxeBuilder({ page })
    .withTags(WCAG_TAGS)
    .disableRules(KNOWN_ISSUES)
    .analyze();

  const blocking = results.violations
    .filter((v) => BLOCKING_IMPACTS.has(v.impact ?? ''))
    .map((v) => ({
      rule: v.id,
      impact: v.impact,
      help: v.help,
      targets: v.nodes.slice(0, 5).map((n) => n.target.join(' ')),
      count: v.nodes.length,
    }));
  expect(blocking).toEqual([]);
});
