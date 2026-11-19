import { useState } from 'react';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ModelPicker } from './ModelPicker';
import type { Choice, ModelCatalog } from '@/lib/models';

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
  expect(trigger).toHaveTextContent('Opus 5.5 · Medium');
  await userEvent.click(trigger);
  const list = screen.getByRole('listbox', { name: 'Models' });
  const groups = within(list).getAllByRole('group');
  expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual([
    'Anthropic',
    'Elsewhere',
  ]);
  const opus = within(list).getByRole('option', { name: /Opus 5.5/ });
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

it('shows the reasoning section of the chosen model', async () => {
  render(
    <Harness initial={{ model: 'opus', options: { effort: 'medium' } }} />,
  );
  await userEvent.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByText('Reasoning')).toBeVisible();
  expect(screen.getByRole('radio', { name: 'Low' })).toBeInTheDocument();
  expect(screen.getByRole('radio', { name: 'Extra High' })).toBeInTheDocument();
  expect(screen.getByRole('radio', { name: 'Max' })).toBeInTheDocument();
  expect(screen.getByRole('radio', { name: 'Medium' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  // Fast mode: Standard (off) is the default.
  expect(screen.getByRole('radio', { name: 'Standard' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  expect(screen.getByRole('radio', { name: 'Fast' })).toHaveAttribute(
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
  // Still open, now showing Sonnet's levels.
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  expect(screen.queryByRole('radio', { name: 'Max' })).not.toBeInTheDocument();
  expect(screen.getByRole('radio', { name: 'High' })).toBeInTheDocument();

  await user.clear(search);
  // The list starts on the default entry: down to Haiku, whose options are
  // unknown.
  await user.keyboard('{ArrowDown}{ArrowDown}{ArrowDown}');
  expect(screen.getByRole('option', { name: /Haiku/ })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await user.keyboard('{Enter}');
  expect(onChange).toHaveBeenLastCalledWith({ model: 'haiku', options: {} });
  expect(screen.getByText(/once the agent has run/)).toBeVisible();
  await user.keyboard('{Escape}');
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveFocus();
});

it('picks the n-th model with Alt+number and the default entry', async () => {
  const onChange = vi.fn();
  render(<Harness initial={{ model: '', options: {} }} onChange={onChange} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: /Model:/ }));
  expect(screen.getByRole('option', { name: /Agent default/ })).toHaveAttribute(
    'data-current',
    'true',
  );
  expect(screen.getByText(/Pick a model to set its reasoning/)).toBeVisible();
  await user.keyboard('{Alt>}2{/Alt}');
  expect(onChange).toHaveBeenLastCalledWith({ model: 'sonnet', options: {} });
});

it('sets effort and fast mode, also with the arrow keys', async () => {
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
  // Still open: options are set in place.
  screen.getByRole('combobox').focus();
  await user.keyboard('{ArrowRight}');
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'xhigh', fast: 'on' },
  });
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveTextContent(
    'Opus 5.5 · Extra High · Fast',
  );
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
