import type { components } from '@/api/schema';

export type ModelCatalog = components['schemas']['ModelCatalog'];
export type CatalogModel = components['schemas']['CatalogModel'];
export type ModelOption = components['schemas']['ModelOption'];
export type ModelChoice = components['schemas']['ModelChoice'];

/** A model and its options; model '' is the default entry of the picker. */
export interface Choice {
  model: string;
  options: Record<string, string>;
}

export const toChoice = (c?: ModelChoice | null): Choice => ({
  model: c?.model ?? '',
  options: { ...(c?.options ?? {}) },
});

export interface ModelGroup {
  name: string;
  models: CatalogModel[];
}

/**
 * The catalog's models matching query (name, id, group, description; every
 * word must match), grouped by their group in catalog order. A current model
 * the catalog does not list comes first as "custom", so an old setting
 * still shows.
 */
export function groupModels(
  catalog: ModelCatalog | undefined,
  query: string,
  current: string,
): ModelGroup[] {
  const models = [...(catalog?.models ?? [])];
  if (current && !models.some((m) => m.id === current)) {
    models.unshift({ id: current, name: current, group: 'Custom' });
  }
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const groups: ModelGroup[] = [];
  for (const m of models) {
    const hay =
      `${m.name} ${m.id} ${m.group ?? ''} ${m.description ?? ''}`.toLowerCase();
    if (!words.every((w) => hay.includes(w))) continue;
    const name = m.group ?? '';
    let g = groups.find((x) => x.name === name);
    if (!g) {
      g = { name, models: [] };
      groups.push(g);
    }
    g.models.push(m);
  }
  return groups;
}

export const isCustom = (catalog: ModelCatalog | undefined, id: string) =>
  !!id && !(catalog?.models ?? []).some((m) => m.id === id);

/** The model's options the picker shows (undefined: not known yet). */
export function optionsOf(
  catalog: ModelCatalog | undefined,
  id: string,
): ModelOption[] | undefined {
  return catalog?.models.find((m) => m.id === id)?.options;
}

/** The value of a toggle that means on. */
export const onValue = (o: ModelOption) => o.values[o.values.length - 1]?.value;

/**
 * The options of choice that the model still has (all of them while its
 * options are unknown), for a switch to model.
 */
export function keepOptions(
  catalog: ModelCatalog | undefined,
  model: string,
  options: Record<string, string>,
): Record<string, string> {
  const known = optionsOf(catalog, model);
  if (!known) return { ...options };
  const out: Record<string, string> = {};
  for (const [id, v] of Object.entries(options)) {
    const o = known.find((x) => x.id === id);
    if (o?.values.some((x) => x.value === v)) out[id] = v;
  }
  return out;
}

export function modelName(
  catalog: ModelCatalog | undefined,
  id: string,
): string {
  return catalog?.models.find((m) => m.id === id)?.name ?? id;
}

/**
 * The compact label of a choice: "Opus 5.5 · Medium", "Swift 1 · High · Fast".
 * fallback names an empty model.
 */
export function choiceLabel(
  catalog: ModelCatalog | undefined,
  c: Choice,
  fallback = 'Default model',
): string {
  const parts = [c.model ? modelName(catalog, c.model) : fallback];
  const known = c.model ? optionsOf(catalog, c.model) : undefined;
  for (const [id, v] of Object.entries(c.options)) {
    const o = known?.find((x) => x.id === id);
    if (!o) {
      if (!known) parts.push(v);
      continue;
    }
    if (o.kind === 'toggle') {
      if (v === onValue(o)) parts.push(o.name.replace(/ mode$/i, ''));
    } else {
      parts.push(o.values.find((x) => x.value === v)?.name ?? v);
    }
  }
  return parts.join(' · ');
}
