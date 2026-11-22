import { useState } from 'react';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { ModelPicker, type HarnessChoice } from './ModelPicker';
import type { Choice, ModelCatalog } from '@/lib/models';

beforeEach(() => localStorage.clear());

const effort = (levels: string[]) => ({
  id: 'effort',
  name: 'Effort',
  kind: 'effort' as const,
  values: levels.map((l) => ({
    value: l,
    name: l === 'xhigh' ? 'Extra High' : l[0].toUpperCase() + l.slice(1),
  })),
});
const fast = {
  id: 'fast',
  name: 'Fast mode',
  kind: 'toggle' as const,
  values: [
    { value: 'off', name: 'Off' },
    { value: 'on', name: 'On' },
  ],
};

const catalog: ModelCatalog = {
  harness: 'claude-code',
  source: 'harness',
  seenAt: new Date().toISOString(),
  switchable: true,
  customModels: false,
  models: [
    {
      id: 'opus',
      name: 'Opus 5.5',
      group: 'Anthropic',
      description: 'Most capable model, for complex work',
      options: [effort(['low', 'medium', 'high', 'xhigh', 'max']), fast],
    },
    {
      id: 'sonnet',
      name: 'Sonnet 5.5',
      group: 'Anthropic',
      options: [effort(['low', 'medium', 'high'])],
    },
    { id: 'haiku', name: 'Haiku 5.5', group: 'Anthropic' },
    { id: 'other/one', name: 'Other One', group: 'Elsewhere', options: [] },
  ],
};

function Harness({
  initial,
  cat = catalog,
  onChange,
}: {
  initial: Choice;
  cat?: ModelCatalog;
  onChange?: (c: Choice) => void;
}) {
  const [v, setV] = useState(initial);
  return (
    <ModelPicker
      catalog={cat}
      value={v}
      defaultLabel="Agent default"
      onChange={(c) => {
        setV(c);
        onChange?.(c);
      }}
    />
  );
}

it('shows the choice compactly: one line per model, no inline descriptions', async () => {
  render(
    <Harness initial={{ model: 'opus', options: { effort: 'medium' } }} />,
  );
  const trigger = screen.getByRole('button', { name: /Model:/ });
  expect(trigger).toHaveAccessibleName('Model: Opus 5.5 · Medium');
  expect(trigger).toHaveTextContent('Opus 5.5 Medium');
  await userEvent.click(trigger);
  const list = screen.getByRole('listbox', { name: 'Models' });
  const groups = within(list).getAllByRole('group');
  expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual([
    'Models',
    'Anthropic',
    'Elsewhere',
  ]);
  const opus = within(list).getByRole('option', { name: 'Opus 5.5' });
  expect(opus).toHaveAttribute('data-current', 'true');
  expect(opus).toHaveAttribute('aria-selected', 'true');
  // The description is behind the info icon, not in the row.
  expect(opus).not.toHaveTextContent('Most capable');
  expect(opus).toHaveAttribute(
    'aria-description',
    'Most capable model, for complex work',
  );
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
  await userEvent.hover(
    within(opus).getByRole('button', { name: 'About Opus 5.5' }),
  );
  expect(screen.getByRole('tooltip')).toHaveTextContent('Most capable model');
  // Models without a description have no icon.
  expect(
    within(list).queryByRole('button', { name: 'About Sonnet 5.5' }),
  ).not.toBeInTheDocument();
});

it('has a rail with Favorites and the harness, and no shortcut badges', async () => {
  render(<Harness initial={{ model: 'opus', options: {} }} />);
  await userEvent.click(screen.getByRole('button', { name: /Model:/ }));
  const rail = screen.getByRole('tablist', { name: 'Model lists' });
  const tabs = within(rail).getAllByRole('tab');
  expect(tabs.map((t) => t.getAttribute('aria-label'))).toEqual([
    'Favorites',
    'Claude Code',
  ]);
  // Without favorites the list is the harness's.
  expect(tabs[1]).toHaveAttribute('aria-selected', 'true');
  expect(screen.getByRole('dialog').querySelector('kbd')).toBeNull();
});

it('shows the reasoning column of the chosen model, with fast mode in the footer', async () => {
  render(
    <Harness initial={{ model: 'opus', options: { effort: 'medium' } }} />,
  );
  await userEvent.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByText('Reasoning')).toBeVisible();
  const levels = screen.getByRole('radiogroup', { name: 'Effort' });
  expect(
    within(levels)
      .getAllByRole('radio')
      .map((r) => r.textContent),
  ).toEqual(['Low', 'Medium', 'High', 'Extra High', 'Max']);
  expect(screen.getByRole('radio', { name: 'Medium' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  // Fast mode: Standard (off) is the default.
  const speed = screen.getByRole('radiogroup', { name: 'Fast mode' });
  expect(
    within(speed).getByRole('radio', { name: 'Standard' }),
  ).toHaveAttribute('aria-checked', 'true');
  expect(within(speed).getByRole('radio', { name: 'Fast' })).toHaveAttribute(
    'aria-checked',
    'false',
  );
});

it('searches, moves with the arrows and picks with Enter, staying open for the reasoning', async () => {
  const onChange = vi.fn();
  render(
    <Harness
      initial={{ model: 'opus', options: { effort: 'max', fast: 'on' } }}
      onChange={onChange}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  const search = screen.getByRole('combobox', { name: 'Search models' });
  expect(search).toHaveFocus();
  await user.type(search, 'son');
  expect(screen.getAllByRole('option')).toHaveLength(1);
  await user.keyboard('{Enter}');
  // Options the new model lacks are dropped (max, fast mode).
  expect(onChange).toHaveBeenLastCalledWith({ model: 'sonnet', options: {} });
  // Still open, now showing Sonnet's levels and no fast mode.
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  expect(screen.queryByRole('radio', { name: 'Max' })).not.toBeInTheDocument();
  expect(screen.queryByRole('radio', { name: 'Fast' })).not.toBeInTheDocument();
  expect(screen.getByRole('radio', { name: 'High' })).toBeInTheDocument();

  await user.clear(search);
  // Back on the current model: down to Haiku, whose options are unknown.
  expect(screen.getByRole('option', { name: 'Sonnet 5.5' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await user.keyboard('{ArrowDown}');
  expect(screen.getByRole('option', { name: 'Haiku 5.5' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await user.keyboard('{Enter}');
  expect(onChange).toHaveBeenLastCalledWith({ model: 'haiku', options: {} });
  expect(screen.getByText(/once the agent has run/)).toBeVisible();
  await user.keyboard('{Home}');
  expect(screen.getByRole('option', { name: 'Agent default' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await user.keyboard('{Escape}');
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveFocus();
});

it('picks the default entry', async () => {
  const onChange = vi.fn();
  render(
    <Harness initial={{ model: 'sonnet', options: {} }} onChange={onChange} />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.click(screen.getByRole('option', { name: 'Agent default' }));
  expect(onChange).toHaveBeenLastCalledWith({ model: '', options: {} });
  expect(screen.getByRole('option', { name: 'Agent default' })).toHaveAttribute(
    'data-current',
    'true',
  );
  expect(screen.getByText(/Pick a model to set its reasoning/)).toBeVisible();
  // Alt+digit no longer picks anything.
  await user.keyboard('{Alt>}2{/Alt}');
  expect(onChange).toHaveBeenCalledTimes(1);
});

it('sets effort and fast mode, also with the arrow keys inside the levels', async () => {
  const onChange = vi.fn();
  render(
    <Harness
      initial={{ model: 'opus', options: { effort: 'medium' } }}
      onChange={onChange}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.click(screen.getByRole('radio', { name: 'High' }));
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'high' },
  });
  await user.click(screen.getByRole('radio', { name: 'Fast' }));
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'high', fast: 'on' },
  });
  await user.click(screen.getByRole('radio', { name: 'Standard' }));
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'high', fast: 'off' },
  });
  await user.click(screen.getByRole('radio', { name: 'Fast' }));
  // Still open: options are set in place. The arrows move within the levels.
  const high = screen.getByRole('radio', { name: 'High' });
  expect(high).toHaveAttribute('tabindex', '0');
  high.focus();
  await user.keyboard('{ArrowDown}');
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'xhigh', fast: 'on' },
  });
  expect(screen.getByRole('radio', { name: 'Extra High' })).toHaveFocus();
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveTextContent(
    'Opus 5.5 Extra High · Fast',
  );
  // The search box leaves the arrows to the list.
  screen.getByRole('combobox').focus();
  await user.keyboard('{ArrowRight}');
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'xhigh', fast: 'on' },
  });
});

it('offers the effort levels of other models while a model is not described', async () => {
  const onChange = vi.fn();
  render(
    <Harness
      initial={{ model: 'haiku', options: {} }}
      cat={{
        ...catalog,
        provisionalOptions: [effort(['low', 'medium', 'high'])],
      }}
      onChange={onChange}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByRole('radio', { name: 'Medium' })).toBeInTheDocument();
  expect(screen.queryByRole('radio', { name: 'Max' })).not.toBeInTheDocument();
  expect(
    screen.getByText(/confirms them once the agent has used/),
  ).toBeVisible();
  await user.click(screen.getByRole('radio', { name: 'High' }));
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'haiku',
    options: { effort: 'high' },
  });
});

it('says a model has no reasoning options when the harness said so', async () => {
  render(<Harness initial={{ model: 'other/one', options: {} }} />);
  await userEvent.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByText(/no reasoning options/)).toBeVisible();
});

it('keeps an unknown model from an old setting as custom', async () => {
  render(<Harness initial={{ model: 'claude-old-1', options: {} }} />);
  const user = userEvent.setup();
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveTextContent(
    'claude-old-1',
  );
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  const custom = screen.getByRole('option', { name: /claude-old-1/ });
  expect(custom).toHaveAttribute('data-current', 'true');
  expect(custom).toHaveTextContent('custom');
});

it('offers a typed id when the harness takes custom models', async () => {
  const onChange = vi.fn();
  render(
    <Harness
      initial={{ model: '', options: {} }}
      cat={{
        ...catalog,
        customModels: true,
        source: 'fallback',
        seenAt: undefined,
      }}
      onChange={onChange}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByText(/Built-in list/)).toBeVisible();
  await user.type(screen.getByRole('combobox'), 'acme/model-9');
  await user.click(screen.getByRole('option', { name: /Use “acme\/model-9”/ }));
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'acme/model-9',
    options: {},
  });
});

it('offers no typed id to users who may not set custom models', async () => {
  render(
    <ModelPicker
      catalog={{ ...catalog, customModels: true }}
      value={{ model: '', options: {} }}
      defaultLabel="Agent default"
      allowCustom={false}
      onChange={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.type(screen.getByRole('combobox'), 'acme/model-9');
  expect(screen.queryByRole('option', { name: /Use/ })).not.toBeInTheDocument();
  expect(screen.getByText('No model matches.')).toBeVisible();
});

const codexCatalog: ModelCatalog = {
  harness: 'codex',
  source: 'harness',
  seenAt: new Date().toISOString(),
  switchable: true,
  customModels: false,
  models: [
    { id: 'gpt-5', name: 'GPT-5', group: 'OpenAI' },
    { id: 'gpt-5-mini', name: 'GPT-5 Mini', group: 'OpenAI' },
  ],
};

function SwitchHarness({
  onSwitch = () => Promise.resolve(),
  others,
  busy,
}: {
  onSwitch?: (harness: string, model: string) => Promise<unknown>;
  others?: HarnessChoice[];
  busy?: boolean;
}) {
  const [v, setV] = useState<Choice>({ model: 'opus', options: {} });
  return (
    <ModelPicker
      catalog={catalog}
      value={v}
      defaultLabel="Agent default"
      onChange={setV}
      harnessSwitch={{
        agentName: 'demo2',
        busy,
        onSwitch,
        loginLink: <a href="/settings/providers">Provider logins.</a>,
        others: others ?? [
          { id: 'codex', name: 'Codex', catalog: codexCatalog },
          {
            id: 'opencode',
            name: 'OpenCode',
            // Never ran on this agent: nothing known of its models.
            catalog: undefined,
          },
        ],
      }}
    />
  );
}

it('gives each other harness a place in the rail: its models, or its default alone', async () => {
  render(<SwitchHarness />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  const rail = screen.getByRole('tablist', { name: 'Model lists' });
  expect(
    within(rail)
      .getAllByRole('tab')
      .map((t) => t.getAttribute('aria-label')),
  ).toEqual(['Favorites', 'Claude Code', 'Codex', 'OpenCode']);
  const list = screen.getByRole('listbox', { name: 'Models' });
  expect(within(list).queryByRole('option', { name: /GPT-5/ })).toBeNull();
  await user.click(within(rail).getByRole('tab', { name: 'Codex' }));
  expect(within(rail).getByRole('tab', { name: 'Codex' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  expect(within(list).getByText(/switches demo2 to Codex/)).toBeVisible();
  expect(
    within(list)
      .getAllByRole('option')
      .map((o) => o.getAttribute('aria-label')),
  ).toEqual(['Default model', 'GPT-5', 'GPT-5 Mini']);
  // OpenCode is unknown: its default alone.
  await user.click(within(rail).getByRole('tab', { name: 'OpenCode' }));
  expect(within(list).getAllByRole('option')).toHaveLength(1);
  expect(within(list).getByText(/Its models show once demo2/)).toBeVisible();
  // Clicking the rail leaves the focus in the search box; Shift+Tab reaches
  // the rail, whose arrows move through it.
  expect(screen.getByRole('combobox')).toHaveFocus();
  await user.tab({ shift: true });
  expect(within(rail).getByRole('tab', { name: 'OpenCode' })).toHaveFocus();
  await user.keyboard('{ArrowUp}');
  expect(within(rail).getByRole('tab', { name: 'Codex' })).toHaveFocus();
  expect(within(list).getAllByRole('option')).toHaveLength(3);
});

it('asks before switching the agent, then switches and closes', async () => {
  const onSwitch = vi.fn().mockResolvedValue(undefined);
  render(<SwitchHarness onSwitch={onSwitch} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.click(screen.getByRole('tab', { name: 'Codex' }));
  await user.click(screen.getByRole('option', { name: 'GPT-5 Mini' }));
  // Nothing happened yet: the confirm names the agent and says all threads.
  expect(onSwitch).not.toHaveBeenCalled();
  expect(screen.getByRole('option', { name: 'GPT-5 Mini' })).toHaveAttribute(
    'data-pending',
    'true',
  );
  const confirm = screen.getByRole('group', { name: 'Switch to Codex' });
  expect(confirm).toHaveTextContent('Switches demo2 to Codex (all threads)');
  expect(confirm).toHaveTextContent('Model: GPT-5 Mini');
  // The reasoning column made room for it.
  expect(screen.queryByText('Reasoning')).toBeNull();
  await user.click(
    within(confirm).getByRole('button', { name: 'Switch to Codex' }),
  );
  expect(onSwitch).toHaveBeenCalledWith('codex', 'gpt-5-mini');
  await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
});

it('switches to a harness without known models on its default', async () => {
  const onSwitch = vi.fn().mockResolvedValue(undefined);
  render(<SwitchHarness onSwitch={onSwitch} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.click(screen.getByRole('tab', { name: 'OpenCode' }));
  await user.click(screen.getByRole('option', { name: 'Default model' }));
  const confirm = screen.getByRole('group', { name: 'Switch to OpenCode' });
  expect(confirm).toHaveTextContent("OpenCode's default model");
  await user.click(within(confirm).getByRole('button', { name: /Switch to/ }));
  expect(onSwitch).toHaveBeenCalledWith('opencode', '');
});

it('keeps the picker open with the error when the switch fails, and Escape backs out of the question first', async () => {
  const onSwitch = vi.fn().mockRejectedValue(new Error('no login'));
  render(<SwitchHarness onSwitch={onSwitch} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.click(screen.getByRole('tab', { name: 'OpenCode' }));
  await user.click(screen.getByRole('option', { name: 'Default model' }));
  await user.click(screen.getByRole('button', { name: 'Switch to OpenCode' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('no login');
  expect(screen.getByRole('dialog')).toBeVisible();
  await user.keyboard('{Escape}');
  expect(
    screen.queryByRole('group', { name: 'Switch to OpenCode' }),
  ).toBeNull();
  expect(screen.getByRole('dialog')).toBeVisible();
  expect(screen.getByText('Reasoning')).toBeVisible();
  await user.keyboard('{Escape}');
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('warns when the new harness has no login and waits while a thread is running', async () => {
  render(
    <SwitchHarness
      busy
      others={[
        {
          id: 'codex',
          name: 'Codex',
          catalog: {
            ...codexCatalog,
            login: { source: 'account', connected: false, mine: true },
          },
        },
      ]}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  const tab = screen.getByRole('tab', { name: 'Codex' });
  expect(tab).toHaveAttribute(
    'aria-description',
    expect.stringContaining('No Codex login'),
  );
  await user.click(tab);
  await user.click(screen.getByRole('option', { name: 'GPT-5' }));
  const confirm = screen.getByRole('group', { name: 'Switch to Codex' });
  expect(confirm).toHaveTextContent('No Codex login: your account has none');
  expect(
    within(confirm).getByRole('link', { name: 'Provider logins.' }),
  ).toBeVisible();
  expect(within(confirm).getByRole('alert')).toHaveTextContent(
    'A thread is still running',
  );
  expect(
    within(confirm).getByRole('button', { name: 'Switch anyway' }),
  ).toBeDisabled();
});

it('searches every harness while there is a query, grouped by harness, and picks with the keyboard', async () => {
  const onSwitch = vi.fn().mockResolvedValue(undefined);
  render(<SwitchHarness onSwitch={onSwitch} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.type(screen.getByRole('combobox'), 'mini');
  const list = screen.getByRole('listbox', { name: 'Models' });
  expect(within(list).getAllByRole('option')).toHaveLength(1);
  expect(within(list).getByRole('group', { name: 'Codex' })).toBeVisible();
  await user.keyboard('{Enter}');
  expect(screen.getByRole('group', { name: 'Switch to Codex' })).toBeVisible();
  await user.keyboard('{Escape}');
  await user.clear(screen.getByRole('combobox'));
  await user.type(screen.getByRole('combobox'), 'opencode');
  expect(within(list).getAllByRole('option')).toHaveLength(1);
  // A harness's name finds all its models.
  await user.clear(screen.getByRole('combobox'));
  await user.type(screen.getByRole('combobox'), 'codex');
  expect(
    within(within(list).getByRole('group', { name: 'Codex' })).getAllByRole(
      'option',
    ),
  ).toHaveLength(3);
  // Choosing a list in the rail ends the search.
  await user.click(screen.getByRole('tab', { name: 'Claude Code' }));
  expect(screen.getByRole('combobox')).toHaveValue('');
});

it('stars models into Favorites, across harnesses, and opens on them next time', async () => {
  const onSwitch = vi.fn().mockResolvedValue(undefined);
  const { unmount } = render(<SwitchHarness onSwitch={onSwitch} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  // Favorites starts empty.
  await user.click(screen.getByRole('tab', { name: 'Favorites' }));
  expect(screen.getByText(/No favorites yet/)).toBeVisible();
  await user.click(screen.getByRole('tab', { name: 'Claude Code' }));
  const sonnet = screen.getByRole('option', { name: 'Sonnet 5.5' });
  await user.click(
    within(sonnet).getByRole('button', { name: 'Add Sonnet 5.5 to favorites' }),
  );
  // Starring does not pick the model.
  expect(sonnet).not.toHaveAttribute('data-current');
  expect(
    within(sonnet).getByRole('button', {
      name: 'Remove Sonnet 5.5 from favorites',
    }),
  ).toHaveAttribute('aria-pressed', 'true');
  await user.click(screen.getByRole('tab', { name: 'Codex' }));
  await user.click(
    within(screen.getByRole('option', { name: 'GPT-5' })).getByRole('button', {
      name: 'Add GPT-5 to favorites',
    }),
  );
  expect(onSwitch).not.toHaveBeenCalled();
  expect(screen.queryByRole('group', { name: /Switch to/ })).toBeNull();
  expect(JSON.parse(localStorage.getItem('agenthold.favoriteModels')!)).toEqual(
    [
      { harness: 'claude-code', model: 'sonnet', name: 'Sonnet 5.5' },
      { harness: 'codex', model: 'gpt-5', name: 'GPT-5' },
    ],
  );
  await user.keyboard('{Escape}');
  unmount();

  // Next time the picker opens on Favorites, each row with its harness.
  render(<SwitchHarness onSwitch={onSwitch} />);
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByRole('tab', { name: 'Favorites' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  const list = screen.getByRole('listbox', { name: 'Models' });
  const rows = within(list).getAllByRole('option');
  expect(rows.map((r) => r.getAttribute('aria-label'))).toEqual([
    'Sonnet 5.5',
    'GPT-5',
  ]);
  expect(rows[0]).toHaveTextContent('Claude Code');
  expect(rows[1]).toHaveTextContent('Codex');
  // A favorite of the current harness is picked, another asks to switch.
  await user.click(rows[0]);
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveTextContent(
    'Sonnet 5.5',
  );
  await user.click(rows[1]);
  expect(screen.getByRole('group', { name: 'Switch to Codex' })).toBeVisible();
  await user.keyboard('{Escape}');
  // Unstarring takes the row out of the list.
  await user.click(
    within(rows[1]).getByRole('button', {
      name: 'Remove GPT-5 from favorites',
    }),
  );
  expect(within(list).getAllByRole('option')).toHaveLength(1);
});

it('shows only favorites of harnesses the picker offers', async () => {
  localStorage.setItem(
    'agenthold.favoriteModels',
    JSON.stringify([{ harness: 'codex', model: 'gpt-5', name: 'GPT-5' }]),
  );
  // No switch: Codex is not offered, so the picker opens on the harness.
  render(<Harness initial={{ model: 'opus', options: {} }} />);
  await userEvent.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByRole('tab', { name: 'Claude Code' })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await userEvent.click(screen.getByRole('tab', { name: 'Favorites' }));
  expect(screen.queryAllByRole('option')).toHaveLength(0);
});

it('reaches the star of the active row with Tab', async () => {
  render(<Harness initial={{ model: 'opus', options: {} }} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  await user.tab();
  expect(
    screen.getByRole('button', { name: 'Add Opus 5.5 to favorites' }),
  ).toHaveFocus();
  await user.keyboard('{Enter}');
  expect(
    screen.getByRole('button', { name: 'Remove Opus 5.5 from favorites' }),
  ).toHaveFocus();
});

it('lists only the current harness without a switch', async () => {
  render(<Harness initial={{ model: 'opus', options: {} }} />);
  await userEvent.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.queryByText(/Switch .* to/)).toBeNull();
});
