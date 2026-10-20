import { expect, test } from '@playwright/test';
import { setTheme, shot } from './helpers';

// Runs after the desktop scenario on the same stand, so there is data to look at.
test('the dashboard works at 375 px without sideways scrolling', async ({ page }, info) => {
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Berlin SOCKS' })).toBeVisible();

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
  await shot(page, info, 'proxies');

  await setTheme(page, 'Dark');
  await shot(page, info, 'proxies-dark');
  await setTheme(page, 'Light');

  for (const [link, heading] of [['Events', 'Events'], ['Settings', 'Settings']] as const) {
    await page.getByRole('link', { name: link, exact: true }).click();
    await expect(page.getByRole('heading', { name: heading, level: 1 })).toBeVisible();
    const o = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(o, `${link} overflows horizontally`).toBeLessThanOrEqual(0);
    await shot(page, info, link.toLowerCase());
  }

  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  await page.getByRole('link', { name: 'Office gateway' }).click();
  await expect(page.getByRole('heading', { name: 'Office gateway' })).toBeVisible();
  const o = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(o, 'detail page overflows horizontally').toBeLessThanOrEqual(0);
  await shot(page, info, 'detail');
});

test('dialogs fit and can be operated on a phone', async ({ page }, info) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Add proxy' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add proxy' });
  await expect(dialog).toBeVisible();
  const box = await dialog.boundingBox();
  expect(box!.width).toBeLessThanOrEqual(375);
  await shot(page, info, 'add-dialog');
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
});
