import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it } from 'bun:test';
import type { Agent, Workflow, WorkflowRun } from '@/api/queries';
import { mockApi, renderApp } from '@/test-utils';

const alice = {
  id: 'u1',
  name: 'Alice',
  email: 'alice@example.com',
  avatarUrl: '',
};

const sampleAgent: Agent = {
  id: 'ag1',
  name: 'builder',
  owner: alice,
  harness: 'claude-code',
  model: 'claude-sonnet-4-6',
  modelOptions: {},
  permissionPolicy: 'ask',
  executorEnabled: true,
  executorApprovalMode: 'model',
  credentialsInSandbox: false,
  login: { source: 'sandbox', connected: true, mine: true },
  cpus: 2,
  memoryMb: 2048,
  idleMinutes: 15,
  state: 'running',
  stateError: '',
  stateChangedAt: '2026-11-20T00:00:00Z',
  needsRebuild: false,
  isTemplate: false,
  role: 'owner',
  createdAt: '2026-11-20T00:00:00Z',
  updatedAt: '2026-11-20T00:00:00Z',
};

const sampleRunNeedsInput: WorkflowRun = {
  id: 'wfr_1',
  workflowId: 'wfl_1',
  workflowName: 'Release pipeline',
  ownerId: 'u1',
  status: 'needs_input',
  triggerKind: 'manual',
  input: { version: '1.2.0' },
  output: {},
  currentStep: 1,
  createdAt: '2026-11-20T10:00:00Z',
  updatedAt: '2026-11-20T10:01:00Z',
  steps: [
    {
      id: 'wfs_1',
      runId: 'wfr_1',
      stepIndex: 0,
      name: 'triage',
      kind: 'agent',
      status: 'completed',
      agentId: 'ag1',
      sessionId: 'ses_live_1',
      askChoices: [],
      output: 'All tests passed.',
    },
    {
      id: 'wfs_2',
      runId: 'wfr_1',
      stepIndex: 1,
      name: 'approve',
      kind: 'ask',
      status: 'waiting',
      askTitle: 'Ship version 1.2.0?',
      askChoices: ['Ship', 'Hold'],
    },
  ],
};

const sampleWorkflow: Workflow = {
  id: 'wfl_1',
  ownerId: 'u1',
  name: 'Release pipeline',
  description: 'Build, test, approve, ship',
  enabled: true,
  hasWebhook: false,
  createdAt: '2026-11-20T09:00:00Z',
  updatedAt: '2026-11-20T09:00:00Z',
  definition: {
    steps: [
      { name: 'triage', kind: 'agent', agentId: 'ag1', prompt: 'Check status' },
      {
        name: 'approve',
        kind: 'ask',
        askTitle: 'Ship?',
        askChoices: ['Ship', 'Hold'],
      },
    ],
  },
};

it('answers a pending approval gate and links agent steps to live threads', async () => {
  let runs: WorkflowRun[] = [sampleRunNeedsInput];
  const { calls } = mockApi({
    'GET /me': { admin: false, user: alice },
    'GET /agents': [sampleAgent],
    'GET /apps': [],
    'GET /workflows': [sampleWorkflow],
    'GET /workflow-runs': () => ({ body: runs }),
    'POST /workflow-runs/wfr_1/steps/wfs_2/answer': ({
      body,
    }: {
      body: unknown;
    }) => {
      const b = body as { choice: string; reason?: string };
      runs = [
        {
          ...sampleRunNeedsInput,
          status: 'running',
          steps: [
            sampleRunNeedsInput.steps[0],
            {
              ...sampleRunNeedsInput.steps[1],
              status: 'completed',
              answerChoice: b.choice,
              answerReason: b.reason,
            },
          ],
        },
      ];
      return { body: runs[0] };
    },
  });

  renderApp('/workflows');

  // Needs you tab shows the waiting approval card and link to the agent step's live thread.
  expect(await screen.findByText('Ship version 1.2.0?')).toBeVisible();
  const threadLink = screen.getByRole('link', { name: /Open thread/ });
  expect(threadLink).toHaveAttribute('href', '/a/ag1?s=ses_live_1');

  // Fill optional reason and click Ship choice button.
  await userEvent.type(
    screen.getByLabelText('Reason for approve'),
    'Looks good to ship',
  );
  await userEvent.click(screen.getByRole('button', { name: 'Ship' }));

  await waitFor(() =>
    expect(
      calls.some(
        (c) => c.key === 'POST /workflow-runs/wfr_1/steps/wfs_2/answer',
      ),
    ).toBe(true),
  );
  const ansCall = calls.find(
    (c) => c.key === 'POST /workflow-runs/wfr_1/steps/wfs_2/answer',
  );
  expect(ansCall?.body).toEqual({
    choice: 'Ship',
    reason: 'Looks good to ship',
  });

  // Switch to Running tab and verify the run now appears with its live thread link.
  await userEvent.click(screen.getByRole('tab', { name: /Running/ }));
  const running = await screen.findByRole('region', {
    name: 'Running workflows',
  });
  expect(
    within(running).getByRole('link', { name: /Open live thread/ }),
  ).toHaveAttribute('href', '/a/ag1?s=ses_live_1');
});

it('builds and saves a new multi-stage workflow definition', async () => {
  let defs: Workflow[] = [];
  const { calls } = mockApi({
    'GET /me': { admin: false, user: alice },
    'GET /agents': [sampleAgent],
    'GET /apps': [],
    'GET /workflows': () => ({ body: defs }),
    'GET /workflow-runs': [],
    'POST /workflows': ({ body }: { body: unknown }) => {
      const b = body as {
        name: string;
        description?: string;
        definition: Workflow['definition'];
      };
      const created: Workflow = {
        id: 'wfl_new',
        ownerId: 'u1',
        name: b.name,
        description: b.description ?? '',
        definition: b.definition,
        enabled: true,
        hasWebhook: !!b.definition.webhookEnabled,
        webhookUrl: b.definition.webhookEnabled
          ? 'https://demo.example/api/v1/workflow-hooks/tok123'
          : undefined,
        createdAt: '2026-11-20T10:00:00Z',
        updatedAt: '2026-11-20T10:00:00Z',
      };
      defs = [created];
      return { status: 201, body: created };
    },
  });

  renderApp('/workflows');
  await userEvent.click(
    await screen.findByRole('button', { name: /New workflow/ }),
  );

  await userEvent.type(screen.getByLabelText('Workflow name'), 'Nightly QA');
  await userEvent.type(
    screen.getByLabelText('Step 1 prompt'),
    'Run diagnostics for {{input}}',
  );

  // Add a shell step and an ask step.
  await userEvent.click(screen.getByRole('button', { name: /Add shell/ }));
  await userEvent.type(screen.getByLabelText('Step 2 command'), 'make test');

  await userEvent.click(screen.getByRole('button', { name: /Add ask/ }));
  await userEvent.type(
    screen.getByLabelText('Step 3 ask title'),
    'Approve nightly report?',
  );

  await userEvent.click(
    screen.getByRole('button', { name: 'Create workflow' }),
  );

  await waitFor(() =>
    expect(calls.some((c) => c.key === 'POST /workflows')).toBe(true),
  );
  expect(await screen.findByText('Nightly QA')).toBeVisible();
});
