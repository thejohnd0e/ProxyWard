import { expect, test, type Page } from '@playwright/test';
import { fixtures, setTheme, shot, telegramMessages, type Fixtures } from './helpers';

// One scenario, many steps: the state (proxies, results, events) builds up
// across the steps, as it would for a real user.
test.describe.configure({ mode: 'serial' });

let fx: Fixtures;
let page: Page;

const row = (name: string) => page.getByRole('row').filter({ hasText: name });
const checkAll = async () => {
  await page.getByRole('button', { name: 'Check all proxies' }).click();
  await expect(page.getByRole('status').filter({ hasText: /availability checks?|already queued/ })).toBeVisible();
};

test.beforeAll(async ({ browser, request }) => {
  fx = await fixtures(request);
  page = await (await browser.newContext({ viewport: { width: 1440, height: 900 }, storageState: 'e2e/.auth/state.json' })).newPage();
  await page.goto('/');
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
});
test.afterAll(async () => {
  await page.context().close();
});

test('starts empty and invites adding a proxy', async ({}, info) => {
  await expect(page.getByText('No proxies yet')).toBeVisible();
  await shot(page, info, 'empty');
});

test('adds a proxy with the form and sees it become Up with its exit IP', async ({}, info) => {
  const office = fx.proxies[0]!;
  await page.getByRole('button', { name: 'Add proxy' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add proxy' });
  await dialog.getByLabel('Name', { exact: true }).fill(office.name);
  await dialog.getByLabel('Host').fill(office.host);
  await dialog.getByLabel('Port').fill(String(office.port));
  await dialog.getByLabel('Tags').fill('lab, office');
  await dialog.getByRole('button', { name: 'Add proxy' }).click();
  await expect(row(office.name)).toBeVisible();
  await expect(row(office.name).getByText('Unknown', { exact: true }).first()).toBeVisible();

  await page.getByRole('button', { name: `Check availability of ${office.name}` }).click();
  await expect(row(office.name).locator('.status--up')).toBeVisible();
  await expect(row(office.name).getByText(office.exit_ip!)).toBeVisible();
  await shot(page, info, 'one-proxy-up');
});

test('previews an import with problems, then imports only the good rows', async ({}, info) => {
  const [office, berlin, paris, wall, dead] = fx.proxies;
  const list = [
    `socks5://${berlin!.username}:${berlin!.password}@${berlin!.host}:${berlin!.port}`,
    `${paris!.protocol}://${paris!.host}:${paris!.port}`,
    `http://${office!.host}:${office!.port}`, // already added
    `http://${wall!.username}:${wall!.password}@${wall!.host}:${wall!.port}`,
    `${dead!.host}:${dead!.port}`,
    'ftp://10.0.0.1:21',
    '10.0.0.2:3128:user:secret',
  ].join('\n');

  await page.getByRole('button', { name: 'Import list' }).click();
  const dialog = page.getByRole('dialog', { name: 'Import list' });
  await dialog.getByLabel('Proxy list').fill(list);
  await dialog.getByRole('button', { name: 'Preview' }).click();

  await expect(dialog.getByText('Ready to import')).toHaveCount(4);
  await expect(dialog.getByText(/already added or repeated/)).toBeVisible();
  await expect(dialog.getByText(/Use http, https or socks5/)).toBeVisible();
  await expect(dialog.getByText(/Write credentials as scheme:\/\/user:password@host:port/)).toBeVisible();
  // Passwords never come back from the server, even in the preview.
  await expect(dialog.getByRole('table')).not.toContainText(berlin!.password!);
  await expect(dialog.getByRole('table')).not.toContainText('secret');
  await shot(page, info, 'import-preview');

  // The paris line uses https, which needs the HTTPS proxy protocol; make it explicit through the default protocol.
  await dialog.getByRole('button', { name: 'Import 4 proxies' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Imported 4 proxies.' })).toBeVisible();
  await expect(page.getByRole('link', { name: '127.0.0.1:' + berlin!.port })).toBeVisible();
});

test('checks every proxy and explains each outcome', async ({}, info) => {
  await checkAll();
  const [office, berlin, paris, wall, dead] = fx.proxies;
  await expect(row(`127.0.0.1:${berlin!.port}`).locator('.status--up')).toBeVisible();
  await expect(row(`127.0.0.1:${paris!.port}`).locator('.status--up')).toBeVisible();
  await expect(row(office!.name).locator('.status--up')).toBeVisible();
  await expect(row(`127.0.0.1:${wall!.port}`).locator('.status--down')).toBeVisible();
  await expect(row(`127.0.0.1:${wall!.port}`).getByText('The proxy rejected the credentials.')).toBeVisible();
  await expect(row(`127.0.0.1:${dead!.port}`).locator('.status--down')).toBeVisible();
  await expect(row(`127.0.0.1:${dead!.port}`).getByText('Could not connect to the proxy.')).toBeVisible();
  await expect(page.getByText(/5 of 100 proxies: 3 up, 2 down/)).toBeVisible();
  await shot(page, info, 'all-checked');
});

test('shows country, city and network without any key, and the source can be chosen', async ({}, info) => {
  await page.getByRole('link', { name: 'Settings' }).click();
  const geo = page.getByRole('region', { name: 'Location lookup' });
  await expect(geo.getByLabel('Source')).toHaveValue('auto');
  await expect(geo.getByLabel(/api key/i)).toHaveCount(0);
  await geo.getByLabel('Source').selectOption('fixture');
  await geo.getByRole('button', { name: 'Save location source' }).click();
  await expect(geo.getByText('Location source saved.')).toBeVisible();

  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  await checkAll();
  await expect(row(fx.proxies[0]!.name).getByText('Berlin, Germany')).toBeVisible();
  await expect(row(`127.0.0.1:${fx.proxies[1]!.port}`).getByText('New York, United States')).toBeVisible();
  await expect(row(`127.0.0.1:${fx.proxies[2]!.port}`).getByText('Tokyo, Japan')).toBeVisible();
  await expect(row(fx.proxies[0]!.name).getByText(/AS64500 Example GmbH/)).toBeVisible();
  await shot(page, info, 'with-geo');
});

test('measures speed and keeps a missing value as a dash', async ({}, info) => {
  await page.getByRole('link', { name: 'Settings' }).click();
  const mon = page.getByRole('region', { name: 'Monitoring' });
  await mon.getByLabel('Speed sample size, MiB').fill('1');
  await mon.getByLabel('Failures before a proxy is reported down').fill('1');
  await mon.getByRole('button', { name: 'Save monitoring settings' }).click();
  await expect(mon.getByText('Monitoring settings saved.')).toBeVisible();

  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  const name = fx.proxies[0]!.name;
  await page.getByRole('button', { name: `Test speed of ${name}` }).click();
  await expect(row(name).getByLabel('Download speed')).toContainText(/\d+(\.\d+)? Mbps/, { timeout: 30_000 });
  await expect(row(name).getByLabel('Upload speed')).toContainText(/\d+(\.\d+)? Mbps/);
  // A proxy that was never speed-tested shows dashes, never "0 Mbps".
  const untested = row(`127.0.0.1:${fx.proxies[1]!.port}`);
  await expect(untested.getByLabel('Download speed')).toContainText('—');
  await expect(untested).not.toContainText('0 Mbps');
  await shot(page, info, 'speed');
});

test('reports down proxies as events and sends them to Telegram once configured', async ({ request }, info) => {
  await checkAll(); // the down threshold is now 1, so the failing proxies open incidents
  await page.getByRole('link', { name: 'Events' }).click();
  await expect(page.getByText('Went down').first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText('Waiting to send').first()).toBeVisible();

  await page.getByRole('link', { name: 'Settings' }).click();
  const tg = page.getByRole('region', { name: 'Telegram notifications' });
  await expect(tg.getByRole('button', { name: 'Send test message' })).toBeDisabled();
  await tg.getByLabel('Bot token').fill(fx.telegram_token);
  await tg.getByLabel('Chat ID').fill(fx.telegram_chat);
  await tg.getByRole('button', { name: 'Save Telegram settings' }).click();
  await expect(tg.getByText('Telegram settings saved.')).toBeVisible();
  await expect(tg.getByLabel('Bot token')).toHaveValue('');
  await expect(tg.getByLabel('Bot token')).toHaveAttribute('placeholder', /token is saved/i);
  await tg.getByRole('button', { name: 'Send test message' }).click();
  await expect(tg.getByText(/Test message queued\./)).toBeVisible();

  await expect.poll(async () => (await telegramMessages(request)).join('\n'), { timeout: 40_000 }).toContain('Test notification');
  await expect.poll(async () => (await telegramMessages(request)).join('\n'), { timeout: 40_000 }).toContain('is down');
  await expect(tg.getByText(/Last message sent/)).toBeVisible({ timeout: 20_000 });

  await page.getByRole('link', { name: 'Events' }).click();
  await expect(page.getByText(/Sent to Telegram/).first()).toBeVisible({ timeout: 20_000 });
  await shot(page, info, 'events');
});

test('shows history and charts on the detail page', async ({}, info) => {
  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  await page.getByRole('link', { name: fx.proxies[0]!.name }).click();
  await expect(page.getByRole('heading', { name: fx.proxies[0]!.name })).toBeVisible();
  await expect(page.getByRole('img', { name: /Availability: \d+ up/ })).toBeVisible();
  await expect(page.getByRole('img', { name: /Speed:/ })).toBeVisible();
  await expect(page.getByRole('table', { name: 'Availability history' }).getByRole('row').nth(1)).toContainText('Up');
  await page.getByRole('tab', { name: 'Speed history' }).click();
  await expect(page.getByRole('table', { name: 'Speed test history' })).toContainText('Mbps');
  await shot(page, info, 'detail');

  await page.getByRole('button', { name: 'Last 7 days' }).click();
  await expect(page.getByRole('button', { name: 'Last 7 days' })).toHaveAttribute('aria-pressed', 'true');
});

test('edits a proxy without exposing or losing its password', async () => {
  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  await page.getByRole('button', { name: /^Edit 127\.0\.0\.1:/ }).nth(0).waitFor();
  const berlin = fx.proxies[1]!;
  await page.getByRole('button', { name: `Edit 127.0.0.1:${berlin.port}` }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit proxy' });
  await expect(dialog.getByLabel('Username')).toHaveValue(berlin.username!);
  await expect(dialog.getByLabel('Password', { exact: true })).toHaveValue('');
  await expect(dialog.getByText(/password is stored/i)).toBeVisible();
  await dialog.getByLabel('Name', { exact: true }).fill('Berlin SOCKS');
  await dialog.getByRole('button', { name: 'Save changes' }).click();
  await expect(row('Berlin SOCKS')).toBeVisible();
  // The stored password still works: the proxy is still reachable after the edit.
  await page.getByRole('button', { name: 'Check availability of Berlin SOCKS' }).click();
  await expect(row('Berlin SOCKS').locator('.status--up')).toBeVisible();
});

test('light and dark themes both render the dashboard', async ({}, info) => {
  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  await setTheme(page, 'Light');
  await shot(page, info, 'light');
  await setTheme(page, 'Dark');
  await shot(page, info, 'dark');
  await page.getByRole('link', { name: fx.proxies[0]!.name }).click();
  await shot(page, info, 'detail-dark');
  await setTheme(page, 'System');
});

test('deleting a proxy asks first and removes it', async () => {
  await page.getByRole('link', { name: 'Proxies', exact: true }).click();
  const dead = `127.0.0.1:${fx.proxies[4]!.port}`;
  await page.getByRole('button', { name: `Delete ${dead}` }).click();
  await page.getByRole('dialog', { name: 'Delete proxy' }).getByRole('button', { name: 'Cancel' }).click();
  await expect(row(dead)).toBeVisible();
  await page.getByRole('button', { name: `Delete ${dead}` }).click();
  await page.getByRole('dialog', { name: 'Delete proxy' }).getByRole('button', { name: 'Delete proxy' }).click();
  await expect(row(dead)).toHaveCount(0);
});
