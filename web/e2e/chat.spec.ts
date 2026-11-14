import { expect, test } from '@playwright/test';

// Signs in through the dev Keycloak, creates an agent with the scripted
// harness, chats with it, and checks a second user cannot see it.
const image = process.env.E2E_IMAGE ?? 'agenthold/base:dev';

async function signIn(page: import('@playwright/test').Page, user: string) {
  await page.goto('/');
  await page.getByRole('link', { name: /Continue with Keycloak/ }).click();
  await page.locator('#username').fill(user);
  await page.locator('#password').fill(user);
  await page.locator('#kc-login').click();
  await expect(page.getByRole('button', { name: 'New agent' })).toBeVisible();
}

test('chat with an agent', async ({ page, browser }) => {
  await signIn(page, 'alice');
  const name = `ui-${Date.now()}`;
  await page.getByRole('button', { name: 'New agent' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(name);
  await dialog.getByLabel('Image').fill(image);
  await dialog.getByLabel('Harness').selectOption('fake');
  await dialog.getByRole('button', { name: 'Create' }).click();

  await expect(page.getByRole('heading', { name })).toBeVisible();
  await expect(page.getByText('Running', { exact: false }).first()).toBeVisible(
    { timeout: 90_000 },
  );

  await page.getByRole('button', { name: 'New session' }).click();
  const box = page.getByRole('textbox', { name: 'Message' });
  await box.fill('hello from the browser');
  await box.press('Enter');
  await expect(page.getByText('echo: hello from the browser')).toBeVisible();

  // Reload mid-way through a longer turn: the stream continues.
  await box.fill('slow 8');
  await box.press('Enter');
  await expect(page.getByText(/chunk 2/)).toBeVisible();
  await page.reload();
  await expect(page.getByText(/chunk 8/)).toBeVisible();
  await expect(page.getByText('done')).toBeVisible();

  // Someone else in another workspace cannot open it.
  const url = page.url();
  const other = await browser.newContext();
  const bob = await other.newPage();
  await signIn(bob, 'bob');
  await bob.goto(url);
  await expect(bob.getByText(/not found/i)).toBeVisible();
  await other.close();

  // Clean up.
  await page.getByRole('tab', { name: /overview/i }).click();
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: 'Delete' }).click();
  const confirm = page
    .getByRole('dialog')
    .getByRole('button', { name: /Delete/ });
  if (await confirm.isVisible().catch(() => false)) await confirm.click();
});
