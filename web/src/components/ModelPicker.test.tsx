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

it('shows the choice compactly and checks the current model', async () => {
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
  // The options of the highlighted model: effort levels and fast mode.
  expect(screen.getByRole('radio', { name: 'Extra High' })).toBeInTheDocument();
  expect(screen.getByRole('radio', { name: 'Medium' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  expect(screen.getByRole('switch', { name: 'Fast mode' })).toHaveAttribute(
    'aria-checked',
    'false',
  );
});

it('searches, moves with the arrows and picks with Enter', async () => {
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
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: /Model:/ })).toHaveFocus();

  await user.click(screen.getByRole('button', { name: /Model:/ }));
  // Sonnet is current; down moves to Haiku, whose options are unknown.
  await user.keyboard('{ArrowDown}');
  expect(screen.getByRole('option', { name: /Haiku/ })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  expect(screen.getByText(/once the agent has used this model/)).toBeVisible();
  await user.keyboard('{Escape}');
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
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
  await user.click(screen.getByRole('switch', { name: 'Fast mode' }));
  expect(onChange).toHaveBeenLastCalledWith({
    model: 'opus',
    options: { effort: 'high', fast: 'on' },
  });
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
