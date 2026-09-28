import { chromium } from '@playwright/test';
import { mkdir } from 'node:fs/promises';

const url = 'http://127.0.0.1:5174';
const out = 'e2e/__screenshots__/redress';
await mkdir(out, { recursive: true });
const browser = await chromium.launch();
for (const [name, width, height] of [['desktop', 1280, 800], ['mobile', 390, 844]]) {
  const page = await browser.newPage({ viewport: { width, height }, baseURL: url });
  const capture = (label) => page.screenshot({ path: `${out}/${name}-${label}.png`, animations: 'disabled' });
  const fresh = async (path) => { await page.goto(path); await page.reload(); };
  await page.request.get(`${url}/__mock/reset`);
  await fresh('/#/kanban');
  await page.getByTestId('card-TASK-103').waitFor();
  await capture('board');
  await fresh('/#/hierarchy?item=TASK-101');
  await page.getByRole('dialog', { name: 'TASK-101' }).waitFor();
  await capture('details');
  await fresh('/#/hierarchy');
  await page.getByRole('button', { name: 'New orchestrator' }).click();
  await page.getByRole('dialog', { name: 'New orchestrator' }).waitFor();
  await capture('orchestrator-empty');
  await page.getByRole('dialog', { name: 'New orchestrator' }).getByRole('combobox', { name: 'Advisor', exact: true }).click();
  await page.getByRole('option', { name: 'No advisor' }).click();
  await capture('orchestrator-no-advisor');
  const all = Array.from({ length: 12 }, (_, i) => ({
    id: `extra${i}`, name: `sample-repo-${String(i + 1).padStart(2, '0')}`,
    path: `/Users/alex/GitHub/sample-repo-${i + 1}`, remote_url: null, remote_owner: null,
    default_branch: 'main', source: 'scan', groups: [], missing: false, dirty: false, last_used_at: null,
  }));
  await page.route('**/api/repos', (route) => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ all, recent: [], groups: [], scanning: false, scanned_at: Date.now() }),
  }));
  await fresh('/#/hierarchy');
  await page.getByRole('button', { name: 'New orchestrator' }).click();
  await page.getByText('sample-repo-12', { exact: true }).waitFor();
  await capture('orchestrator-12-repos');
  await page.unroute('**/api/repos');
  await fresh('/#/hierarchy');
  await page.getByRole('button', { name: 'New item' }).click();
  await page.getByRole('menuitem', { name: 'Story' }).click();
  const item = page.getByRole('dialog', { name: 'New item' });
  await item.waitFor();
  await capture('new-item');
  await item.getByRole('combobox', { name: 'Parent' }).click();
  await page.getByRole('option', { name: /EPIC-12/ }).click();
  await item.getByRole('textbox', { name: 'Title' }).fill('Two-factor login');
  await item.getByRole('button', { name: 'Create item' }).click();
  await page.getByText(/Created STORY-\d+/).waitFor();
  await capture('success-toast');
  await fresh('/#/kanban?level=top');
  await page.getByTestId('card-SPIKE-3').getByRole('button', { name: 'Move to… SPIKE-3' }).focus();
  await page.keyboard.press('Enter');
  await page.getByRole('menuitem', { name: /^Done/ }).click();
  await page.getByRole('region', { name: /Notifications/ }).getByText('This spike reaches Done after materialization.').waitFor();
  await capture('error-toast');
  console.log(`${name} captured`);
  await page.close();
}
await browser.close();
