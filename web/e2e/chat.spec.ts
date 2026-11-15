import { expect, test } from '@playwright/test';

// The native dev VM provides the local admin and scripted test harness.
async function signIn(page: import('@playwright/test').Page) {
  await page.goto('/');
  await page.getByLabel('Username').fill(process.env.E2E_USER ?? 'admin');
  await page.getByLabel('Password').fill(process.env.E2E_PASSWORD ?? 'admin');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('button', { name: 'New agent' })).toBeVisible();
}

test('chat with an agent', async ({ page }) => {
  await signIn(page);
  const name = `ui-${Date.now()}`;
  await page.getByRole('button', { name: 'New agent' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(name);
  await dialog.getByLabel('Harness').selectOption('fake');
  await dialog.getByRole('button', { name: 'Create' }).click();

  // A new agent opens on a blank chat; the sidebar lists it.
  await expect(
    page.getByRole('heading', { name: `Start a chat with ${name}` }),
  ).toBeVisible();
  await expect(
    page.getByRole('list', { name: 'Agents' }).getByText(name),
  ).toBeVisible();
  await expect(page.getByRole('img', { name: 'running' }).first()).toBeVisible({
    timeout: 90_000,
  });

  // The first message creates the session.
  const box = page.getByRole('textbox', { name: 'Message' });
  await box.fill('hello from the browser');
  await box.press('Enter');
  await expect(page).toHaveURL(/[?&]s=ses_/);
  await expect(page.getByText('echo: hello from the browser')).toBeVisible();
  await expect(
    page
      .getByRole('list', { name: 'Chats' })
      .getByText('hello from the browser'),
  ).toBeVisible();

  // Reload mid-way through a longer turn: the stream continues.
  await box.fill('slow 8');
  await box.press('Enter');
  await expect(page.getByText(/chunk 2/)).toBeVisible();
  await page.reload();
  await expect(page.getByText(/chunk 8/)).toBeVisible();
  await expect(page.getByText('done')).toBeVisible();

  // The agent panel lists the session next to the chat.
  await page.getByRole('button', { name: 'Agent panel' }).click();
  await expect(
    page
      .getByRole('complementary', { name: 'Agent panel' })
      .getByRole('list', { name: 'Sessions' })
      .getByText('hello from the browser'),
  ).toBeVisible();

  const agentPath = new URL(page.url()).pathname;

  // Clean up from the agent's settings.
  const agentId = agentPath.split('/')[2];
  await page.goto(agentPath);
  await page.getByRole('link', { name: 'Agent settings' }).click();
  await page.getByRole('button', { name: 'Delete' }).click();
  await page
    .getByRole('dialog')
    .getByRole('button', { name: 'Delete agent' })
    .click();
  // Deleting is asynchronous; the app must leave the agent, not land back in it.
  await expect(page).not.toHaveURL(new RegExp(agentId));
});
