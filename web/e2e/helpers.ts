import { expect, type APIRequestContext, type Page, type TestInfo } from '@playwright/test';

export const CONTROL = 'http://127.0.0.1:18081';

export interface Fixtures {
  admin_password: string;
  geo_key: string;
  telegram_token: string;
  telegram_chat: string;
  proxies: Array<{ name: string; protocol: string; host: string; port: number; username?: string; password?: string; exit_ip?: string }>;
}

export async function fixtures(request: APIRequestContext): Promise<Fixtures> {
  const res = await request.get(`${CONTROL}/__e2e/fixtures`);
  expect(res.ok()).toBeTruthy();
  return (await res.json()) as Fixtures;
}

export async function telegramMessages(request: APIRequestContext): Promise<string[]> {
  const res = await request.get(`${CONTROL}/__e2e/telegram`);
  return ((await res.json()) as { messages: string[] | null }).messages ?? [];
}

export async function signIn(page: Page, password: string): Promise<void> {
  await page.goto('/');
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
}

/** Saves a screenshot for visual review (not an assertion). */
export async function shot(page: Page, info: TestInfo, name: string): Promise<void> {
  await page.screenshot({ path: `e2e-screenshots/${info.project.name}-${name}.png`, fullPage: false });
}

export async function setTheme(page: Page, theme: 'System' | 'Light' | 'Dark'): Promise<void> {
  await page.getByLabel('Theme').selectOption({ label: theme });
}
