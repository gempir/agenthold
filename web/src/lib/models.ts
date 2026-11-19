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

export interface ShownOptions {
  options: ModelOption[];
  /** Not reported for this model: the levels other models of the harness have. */
  provisional: boolean;
}

/**
 * What the picker offers for a model: its own options, or while the harness
 * has not described it, the effort levels it reported for other models
 * (the harness refuses a level the model lacks, with a notice).
 */
export function modelOptionsFor(
  catalog: ModelCatalog | undefined,
  id: string,
): ShownOptions | undefined {
  if (!id) return undefined;
  const known = optionsOf(catalog, id);
  if (known) return { options: known, provisional: false };
  const prov = catalog?.provisionalOptions;
  return prov?.length ? { options: prov, provisional: true } : undefined;
}

/** The value of a toggle that means on. */
export const onValue = (o: ModelOption) => o.values[o.values.length - 1]?.value;

/** "Extra High" for xhigh, whatever the harness calls it ("Xhigh"). */
export const valueLabel = (
  o: ModelOption,
  v: { value: string; name: string },
) =>
  o.kind === 'effort' && /^x-?high$/i.test(v.value) ? 'Extra High' : v.name;

/** The labels of a fast mode toggle's two states. */
export const toggleLabels = (o: ModelOption): [string, string] =>
  /fast/i.test(`${o.id} ${o.name}`) ? ['Standard', 'Fast'] : ['Off', 'On'];

/**
 * The options of choice that the model still has (all of them while its
 * options are unknown; while only the provisional levels are, the levels
 * among them), for a switch to model.
 */
export function keepOptions(
  catalog: ModelCatalog | undefined,
  model: string,
  options: Record<string, string>,
): Record<string, string> {
  const shown = modelOptionsFor(catalog, model);
  if (!shown) return { ...options };
  const out: Record<string, string> = {};
  for (const [id, v] of Object.entries(options)) {
    const o = shown.options.find((x) => x.id === id);
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
  const shown = modelOptionsFor(catalog, c.model);
  for (const [id, v] of Object.entries(c.options)) {
    const o = shown?.options.find((x) => x.id === id);
    if (!o) {
      if (!shown) parts.push(v);
      continue;
    }
    if (o.kind === 'toggle') {
      if (v === onValue(o)) parts.push(o.name.replace(/ mode$/i, ''));
    } else {
      const value = o.values.find((x) => x.value === v);
      parts.push(value ? valueLabel(o, value) : v);
    }
  }
  return parts.join(' · ');
}
