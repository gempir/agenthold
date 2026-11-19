import { expect, it } from 'vitest';
import {
  choiceLabel,
  groupModels,
  keepOptions,
  type ModelCatalog,
} from './models';

const catalog: ModelCatalog = {
  harness: 'opencode',
  source: 'harness',
  switchable: true,
  customModels: true,
  models: [
    {
      id: 'anthropic/opus',
      name: 'Opus',
      group: 'Anthropic',
      options: [
        {
          id: 'effort',
          name: 'Effort',
          kind: 'effort',
          values: [
            { value: 'low', name: 'Low' },
            { value: 'high', name: 'High' },
          ],
        },
      ],
    },
    { id: 'openai/gpt', name: 'GPT', group: 'OpenAI' },
  ],
};

it('groups and filters models, every word must match', () => {
  expect(groupModels(catalog, '', '').map((g) => g.name)).toEqual([
    'Anthropic',
    'OpenAI',
  ]);
  expect(groupModels(catalog, 'openai g', '')[0].models[0].id).toBe(
    'openai/gpt',
  );
  expect(groupModels(catalog, 'opus openai', '')).toEqual([]);
  expect(groupModels(catalog, '', 'x/old')[0]).toMatchObject({
    name: 'Custom',
    models: [{ id: 'x/old' }],
  });
});

it('labels choices and keeps only valid options', () => {
  expect(
    choiceLabel(catalog, {
      model: 'anthropic/opus',
      options: { effort: 'high' },
    }),
  ).toBe('Opus · High');
  expect(choiceLabel(catalog, { model: '', options: {} }, 'Default')).toBe(
    'Default',
  );
  // Unknown options are shown as given while the model's options are unknown.
  expect(
    choiceLabel(catalog, { model: 'openai/gpt', options: { e: 'x' } }),
  ).toBe('GPT · x');
  expect(keepOptions(catalog, 'anthropic/opus', { effort: 'max' })).toEqual({});
  expect(keepOptions(catalog, 'openai/gpt', { effort: 'max' })).toEqual({
    effort: 'max',
  });
});
