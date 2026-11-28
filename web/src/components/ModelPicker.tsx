import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import {
  Check,
  ChevronDown,
  Info,
  Loader2,
  Search,
  Star,
  TriangleAlert,
} from 'lucide-react';
import { BrandIcon, HarnessIcon } from '@/components/HarnessIcon';
import { Button } from '@/components/ui/button';
import {
  isFavorite,
  useFavoriteModels,
  type FavoriteModel,
} from '@/lib/favorites';
import {
  harnessBrand,
  harnessLabel,
  providerBrand,
  type Brand,
} from '@/lib/harnesses';
import {
  choiceLabel,
  choiceParts,
  groupModels,
  isCustom,
  keepOptions,
  modelName,
  modelOptionsFor,
  onValue,
  toggleLabels,
  valueLabel,
  type CatalogModel,
  type Choice,
  type ModelCatalog,
  type ModelOption,
} from '@/lib/models';
import { cn } from '@/lib/utils';

/** A harness the agent can be switched to, with what is known of its models. */
export interface HarnessChoice {
  id: string;
  name: string;
  iconUrl?: string;
  /** Its catalog: what it reported, or the built-in list; undefined while unknown. */
  catalog?: ModelCatalog;
}

/** A model target chosen for parallel multi-model thread launch on a draft. */
export interface DraftModelTarget {
  harness: string;
  model: string;
  options: Record<string, string>;
  label: string;
}

/**
 * Lets the picker switch the harness: the other harnesses get their
 * own place in the rail, and picking one of their models asks to confirm.
 * When `scope` is `'thread'`, the switch applies only to the current thread.
 */
export interface HarnessSwitch {
  agentName: string;
  others: HarnessChoice[];
  /** 'thread' switches only this thread; 'agent' (default) switches the agent default. */
  scope?: 'thread' | 'agent';
  /** Does the switch (rejects with the reason it could not). */
  onSwitch: (harness: string, model: string) => Promise<unknown>;
  /** A conversation is running: the harness cannot be stopped now. */
  busy?: boolean;
  /** Where to connect a login, shown with a missing-login warning. */
  loginLink?: ReactNode;
}

type Entry =
  | { kind: 'default' }
  | { kind: 'model'; id: string }
  /** A typed model id (harnesses that take any). */
  | { kind: 'custom'; id: string }
  /** A model of another harness ('' for its default). */
  | { kind: 'other'; harness: string; id: string };

const entryKey = (e: Entry) => {
  switch (e.kind) {
    case 'default':
      return 'default';
    case 'other':
      return `o:${e.harness}:${e.id}`;
    default:
      return `${e.kind}:${e.id}`;
  }
};

/** The model of an entry of the current harness. */
const entryModel = (e: Entry) =>
  e.kind === 'model' || e.kind === 'custom' ? e.id : '';

const isCurrentEntry = (e: Entry) =>
  e.kind === 'default' || e.kind === 'model' || e.kind === 'custom';

/** One line of the list. */
interface Row {
  entry: Entry;
  title: string;
  description?: string;
  tag?: string;
  detail?: string;
  icon: Brand;
  /** What starring the row keeps (models only). */
  star?: FavoriteModel;
}

/** Rows under one heading: a provider group, or a harness while searching. */
interface Section {
  key: string;
  label?: string;
  /** The heading's icon. */
  harness?: string;
  rows: Row[];
}

/** What the rail shows: the starred models or one harness's. */
type View = { kind: 'favorites' } | { kind: 'harness'; id: string };

const words = (q: string) => q.toLowerCase().split(/\s+/).filter(Boolean);

const modelIcon = (harness: string, m: { id: string; group?: string }) =>
  providerBrand(m.group, m.id) ?? harnessBrand(harness);

/**
 * What switching to a harness would find missing: no login the harness could
 * start with (an account login that is not connected or broken).
 */
function loginProblem(h: HarnessChoice): string | undefined {
  const l = h.catalog?.login;
  if (!l || l.source !== 'account') return undefined;
  const whose = l.mine
    ? 'your account has'
    : `${l.user?.name ?? 'the login owner'}'s account has`;
  if (!l.connected) {
    return l.reason ?? `No ${h.name} login: ${whose} none connected.`;
  }
  if (l.status === 'error') {
    return `The ${h.name} login of ${l.mine ? 'your account' : (l.user?.name ?? 'the login owner')} needs to be connected again.`;
  }
  return undefined;
}

/**
 * The model picker: a compact trigger (the harness's logo, "Opus 5.5 Medium")
 * that opens a popover in three columns. A rail on the left holds the starred
 * models and one entry per harness; the middle lists the models of the one
 * chosen there, under a search box that searches every harness; the right
 * column sets the reasoning effort of the current model. Toggles such as
 * fast mode sit in the footer. Choices apply at once and the popover stays
 * open, so a model and its reasoning are set in one go. In the list, arrows
 * move, Enter picks and Escape closes; the star of a row keeps it in
 * Favorites (in this browser).
 *
 * With harnessSwitch the other harnesses join the rail; picking one of their
 * models asks to switch the agent (all its threads) to that harness.
 */
export function ModelPicker({
  catalog,
  value,
  onChange,
  defaultLabel,
  defaultDescription,
  disabled,
  allowCustom = true,
  side = 'top',
  label = 'Model',
  harness,
  harnessSwitch,
  multiSelect = false,
  selectedModels = [],
  onSelectedModelsChange,
  className,
}: {
  catalog: ModelCatalog | undefined;
  value: Choice;
  onChange: (c: Choice) => void;
  /** The entry for model '' ("Agent default", "Harness default"). */
  defaultLabel: string;
  defaultDescription?: string;
  disabled?: boolean;
  /** Offer typed model ids (harnesses that take them); owners only. */
  allowCustom?: boolean;
  side?: 'top' | 'bottom';
  /** Accessible name of the trigger. */
  label?: string;
  /** The harness the catalog is of (its id is also in the catalog). */
  harness?: { id: string; name: string; iconUrl?: string };
  harnessSwitch?: HarnessSwitch;
  /** Allow Shift+clicking multiple models to launch parallel threads (drafts only). */
  multiSelect?: boolean;
  selectedModels?: DraftModelTarget[];
  onSelectedModelsChange?: (models: DraftModelTarget[]) => void;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };

  const current = {
    id: harness?.id ?? catalog?.harness ?? '',
    name: harness?.name ?? harnessLabel(harness?.id ?? catalog?.harness ?? ''),
    iconUrl: harness?.iconUrl,
  };
  const [name, ...rest] = choiceParts(catalog, value, defaultLabel);
  const isMulti = multiSelect && selectedModels.length > 1;
  const accessibleChoice = isMulti
    ? `${selectedModels.length} models (${selectedModels.map((m) => m.label).join(', ')})`
    : choiceLabel(catalog, value, defaultLabel);
  return (
    <div ref={root} className={cn('relative min-w-0', className)}>
      <button
        ref={trigger}
        type="button"
        data-model-picker-trigger="true"
        aria-label={`${label}: ${accessibleChoice}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        disabled={disabled}
        title={current.name || undefined}
        onClick={() => setOpen((o) => !o)}
        className="flex max-w-full min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-left hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-60"
      >
        <HarnessIcon
          harness={current.id}
          iconUrl={current.iconUrl}
          className="size-3.5"
        />
        {isMulti ? (
          <span className="flex min-w-0 items-center gap-1 truncate">
            <span className="truncate">{selectedModels[0].label}</span>
            <span className="shrink-0 rounded-full bg-primary/15 px-1.5 py-0.5 text-[10px] font-medium text-primary">
              +{selectedModels.length - 1}
            </span>
          </span>
        ) : (
          <span className="truncate">
            {name}
            {rest.length > 0 && (
              <>
                {' '}
                <span className="text-muted-foreground">
                  {rest.join(' · ')}
                </span>
              </>
            )}
          </span>
        )}
        <ChevronDown className="size-3 shrink-0 opacity-70" aria-hidden />
      </button>
      {open && (
        <PickerPanel
          catalog={catalog}
          value={value}
          onChange={onChange}
          onClose={close}
          defaultLabel={defaultLabel}
          defaultDescription={defaultDescription}
          allowCustom={allowCustom}
          side={side}
          label={label}
          current={current}
          harnessSwitch={harnessSwitch}
          multiSelect={multiSelect}
          selectedModels={selectedModels}
          onSelectedModelsChange={onSelectedModelsChange}
        />
      )}
    </div>
  );
}

function PickerPanel({
  catalog,
  value,
  onChange,
  onClose,
  defaultLabel,
  defaultDescription,
  allowCustom,
  side,
  label,
  current,
  harnessSwitch,
  multiSelect,
  selectedModels,
  onSelectedModelsChange,
}: {
  catalog: ModelCatalog | undefined;
  value: Choice;
  onChange: (c: Choice) => void;
  onClose: () => void;
  defaultLabel: string;
  defaultDescription?: string;
  allowCustom: boolean;
  side: 'top' | 'bottom';
  label: string;
  current: { id: string; name: string; iconUrl?: string };
  harnessSwitch?: HarnessSwitch;
  multiSelect?: boolean;
  selectedModels?: DraftModelTarget[];
  onSelectedModelsChange?: (models: DraftModelTarget[]) => void;
}) {
  const id = useId();
  const listId = `${id}-list`;
  const { favorites, toggle: toggleFavorite } = useFavoriteModels();
  const [query, setQuery] = useState('');
  const [pending, setPending] = useState<{ harness: string; model: string }>();
  const [activeKey, setActiveKey] = useState<string>();
  const input = useRef<HTMLInputElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);

  // The current harness first, then those the agent can switch to.
  const rail: HarnessChoice[] = [
    { id: current.id, name: current.name, iconUrl: current.iconUrl, catalog },
    ...(harnessSwitch?.others ?? []),
  ];
  const harnessOf = (h: string) => rail.find((x) => x.id === h);
  const starred = favorites.filter((f) => harnessOf(f.harness));
  // Favorites first when there are any to pick from.
  const [view, setView] = useState<View>(() =>
    starred.length > 0
      ? { kind: 'favorites' }
      : { kind: 'harness', id: current.id },
  );
  const viewed =
    view.kind === 'harness' ? (harnessOf(view.id) ?? rail[0]) : undefined;

  const q = query.trim();
  const w = words(q);
  const matches = (s: string) => w.every((x) => s.toLowerCase().includes(x));

  // Back to the list, with the search box focused again for the keyboard.
  const cancelPending = () => {
    setPending(undefined);
    input.current?.focus();
  };

  const starOf = (h: string, m: CatalogModel): FavoriteModel => ({
    harness: h,
    model: m.id,
    name: m.name,
  });

  /** The rows of the current harness: all of them, or those matching. */
  const currentRows = (): Row[] => {
    const nameMatch = !!q && matches(current.name);
    const groups = groupModels(catalog, nameMatch ? '' : query, value.model);
    const rows: Row[] = [];
    if (!q || nameMatch || matches(defaultLabel)) {
      rows.push({
        entry: { kind: 'default' },
        title: defaultLabel,
        description: defaultDescription,
        icon: harnessBrand(current.id),
      });
    }
    for (const g of groups) {
      for (const m of g.models) {
        rows.push({
          entry: { kind: 'model', id: m.id },
          title: m.name,
          description: m.description,
          tag: isCustom(catalog, m.id) ? 'custom' : undefined,
          icon: modelIcon(current.id, m),
          star: starOf(current.id, m),
        });
      }
    }
    const custom =
      allowCustom &&
      !!catalog?.customModels &&
      !!q &&
      !/\s/.test(q) &&
      !rows.some((r) => r.entry.kind === 'model' && r.entry.id === q);
    if (custom) {
      rows.push({
        entry: { kind: 'custom', id: q },
        title: `Use “${q}”`,
        description: 'A model id the harness does not list',
        icon: harnessBrand(current.id),
      });
    }
    return rows;
  };

  /** The rows of another harness: its default, then its models. */
  const otherRows = (h: HarnessChoice): Row[] => {
    const nameMatch = !!q && matches(h.name);
    const rows: Row[] = [];
    if (!q || matches(`${h.name} agent default model`)) {
      rows.push({
        entry: { kind: 'other', harness: h.id, id: '' },
        title: 'Default model',
        description: `Switches the agent to ${h.name} on its own default model`,
        icon: harnessBrand(h.id),
      });
    }
    for (const g of groupModels(h.catalog, nameMatch ? '' : query, '')) {
      for (const m of g.models) {
        rows.push({
          entry: { kind: 'other', harness: h.id, id: m.id },
          title: m.name,
          description: m.description,
          icon: modelIcon(h.id, m),
          star: starOf(h.id, m),
        });
      }
    }
    return rows;
  };

  const favoriteRows = (): Row[] =>
    starred.map((f) => {
      const h = harnessOf(f.harness)!;
      const m: CatalogModel = h.catalog?.models.find(
        (x) => x.id === f.model,
      ) ?? { id: f.model, name: f.name };
      return {
        entry:
          h.id === current.id
            ? { kind: 'model', id: f.model }
            : { kind: 'other', harness: h.id, id: f.model },
        title: m.name,
        description: m.description,
        detail: h.name,
        icon: modelIcon(h.id, m),
        star: starOf(h.id, m),
      };
    });

  /** A harness's models under their provider headings (when there are several). */
  const groupedSections = (h: HarnessChoice): Section[] => {
    const isCurrent = h.id === current.id;
    const rows = isCurrent ? currentRows() : otherRows(h);
    const groups = groupModels(h.catalog, '', isCurrent ? value.model : '');
    const named = groups.filter((g) => g.name);
    if (named.length < 2) return [{ key: `h:${h.id}`, rows }];
    const groupOf = (r: Row) => {
      const model = r.entry.kind === 'default' ? '' : r.entry.id;
      return groups.find((g) => g.models.some((m) => m.id === model))?.name;
    };
    const sections: Section[] = [];
    for (const r of rows) {
      const g = groupOf(r);
      const last = sections[sections.length - 1];
      if (last && last.label === g) last.rows.push(r);
      else sections.push({ key: `g:${h.id}:${g ?? ''}`, label: g, rows: [r] });
    }
    return sections;
  };

  let sections: Section[];
  if (q) {
    // A search goes through every harness, each under its own heading.
    sections = rail
      .map((h) => ({
        key: `h:${h.id}`,
        label: rail.length > 1 ? h.name : undefined,
        harness: h.id,
        rows: h.id === current.id ? currentRows() : otherRows(h),
      }))
      .filter((s) => s.rows.length > 0);
  } else if (view.kind === 'favorites') {
    sections = [{ key: 'favorites', rows: favoriteRows() }];
  } else {
    sections = groupedSections(viewed!);
  }
  const rows = sections.flatMap((s) => s.rows);

  const currentIndex = rows.findIndex(
    (r) => isCurrentEntry(r.entry) && entryModel(r.entry) === value.model,
  );
  const found = activeKey
    ? rows.findIndex((r) => entryKey(r.entry) === activeKey)
    : -1;
  const index =
    rows.length === 0
      ? -1
      : found >= 0
        ? found
        : q
          ? 0
          : Math.max(0, currentIndex);
  const activeRow = index >= 0 ? rows[index] : undefined;
  const moveTo = (i: number) => setActiveKey(entryKey(rows[i].entry));

  const targetOfEntry = (e: Entry): DraftModelTarget => {
    if (e.kind === 'other') {
      const h = harnessOf(e.harness);
      const lbl = e.id
        ? modelName(h?.catalog, e.id)
        : `${h?.name ?? e.harness} default`;
      return { harness: e.harness, model: e.id, options: {}, label: lbl };
    }
    const m = entryModel(e);
    const opts = keepOptions(catalog, m, value.options);
    return {
      harness: current.id,
      model: m,
      options: opts,
      label: choiceLabel(catalog, { model: m, options: opts }, defaultLabel),
    };
  };

  const toggleMulti = (e: Entry) => {
    if (!onSelectedModelsChange) return;
    setPending(undefined);
    const target = targetOfEntry(e);
    const base: DraftModelTarget[] =
      selectedModels && selectedModels.length > 0
        ? selectedModels
        : [
            {
              harness: current.id,
              model: value.model,
              options: value.options,
              label: choiceLabel(catalog, value, defaultLabel),
            },
          ];
    const exists = base.some(
      (x) => x.harness === target.harness && x.model === target.model,
    );
    const next = exists
      ? base.filter(
          (x) => !(x.harness === target.harness && x.model === target.model),
        )
      : [...base, target];
    if (next.length <= 1) {
      onSelectedModelsChange([]);
      if (next.length === 1 && next[0].harness === current.id) {
        onChange({ model: next[0].model, options: next[0].options });
      }
    } else {
      onSelectedModelsChange(next);
    }
  };

  const pick = (model: string) => {
    setPending(undefined);
    if (selectedModels?.length) onSelectedModelsChange?.([]);
    onChange({ model, options: keepOptions(catalog, model, value.options) });
  };
  const activate = (e: Entry, shiftKey = false) => {
    if (multiSelect && shiftKey && onSelectedModelsChange) {
      toggleMulti(e);
      return;
    }
    if (e.kind === 'other') setPending({ harness: e.harness, model: e.id });
    else pick(entryModel(e));
  };
  const setOption = (o: ModelOption, v: string) =>
    onChange({ model: value.model, options: { ...value.options, [o.id]: v } });
  const chooseView = (v: View) => {
    setView(v);
    setQuery('');
    setActiveKey(undefined);
  };

  const optionId = (i: number) => `${id}-opt-${i}`;
  useEffect(() => {
    document
      .getElementById(optionId(index))
      ?.scrollIntoView?.({ block: 'nearest' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index, view]);

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const n = rows.length;
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        if (n) moveTo((index + 1) % n);
        break;
      case 'ArrowUp':
        e.preventDefault();
        if (n) moveTo((index - 1 + n) % n);
        break;
      case 'Home':
        if (!q && n) {
          e.preventDefault();
          moveTo(0);
        }
        break;
      case 'End':
        if (!q && n) {
          e.preventDefault();
          moveTo(n - 1);
        }
        break;
      case 'Enter':
        e.preventDefault();
        if (activeRow) activate(activeRow.entry, e.shiftKey);
        break;
    }
  };

  const renderRow = (r: Row) => {
    const key = entryKey(r.entry);
    const i = rows.indexOf(r);
    const model = isCurrentEntry(r.entry) ? entryModel(r.entry) : undefined;
    const isMultiSelected =
      !!multiSelect &&
      !!selectedModels &&
      selectedModels.length > 1 &&
      selectedModels.some((x) =>
        r.entry.kind === 'other'
          ? x.harness === r.entry.harness && x.model === r.entry.id
          : x.harness === current.id && x.model === model,
      );
    const isCurrent =
      !(multiSelect && selectedModels && selectedModels.length > 1) &&
      model !== undefined &&
      model === value.model;
    const chosenForSwitch =
      r.entry.kind === 'other' &&
      pending?.harness === r.entry.harness &&
      pending.model === r.entry.id;
    const starredRow =
      !!r.star && isFavorite(favorites, r.star.harness, r.star.model);
    return (
      <li
        key={key}
        id={optionId(i)}
        role="option"
        aria-selected={i === index}
        aria-label={r.title}
        aria-description={r.description}
        data-current={isCurrent || isMultiSelected || undefined}
        data-multi-selected={isMultiSelected || undefined}
        data-pending={chosenForSwitch || undefined}
        title={r.entry.kind === 'default' ? undefined : r.entry.id || undefined}
        onMouseMove={() => i !== index && setActiveKey(key)}
        onMouseDown={(ev) => ev.preventDefault()}
        onClick={(ev) => activate(r.entry, ev.shiftKey)}
        className={cn(
          'group flex h-10 cursor-pointer items-center gap-2.5 rounded-lg px-2.5 text-sm',
          i === index && 'bg-accent',
        )}
      >
        <BrandIcon brand={r.icon} />
        <span className="min-w-0 truncate">{r.title}</span>
        {(isCurrent || isMultiSelected || chosenForSwitch) && (
          <Check
            className={cn(
              'size-4 shrink-0',
              (chosenForSwitch || isMultiSelected) && 'text-sky-500',
            )}
            aria-hidden
          />
        )}
        <span className="flex-1" />
        {r.detail && (
          <span className="shrink-0 truncate text-xs text-muted-foreground">
            {r.detail}
          </span>
        )}
        {r.tag && (
          <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground uppercase">
            {r.tag}
          </span>
        )}
        {r.description && <InfoTip text={r.description} label={r.title} />}
        {r.star && (
          <button
            type="button"
            // The active row's star is the one Tab reaches from the search.
            tabIndex={i === index ? 0 : -1}
            aria-label={
              starredRow
                ? `Remove ${r.title} from favorites`
                : `Add ${r.title} to favorites`
            }
            aria-pressed={starredRow}
            onMouseDown={(ev) => ev.preventDefault()}
            onClick={(ev) => {
              ev.stopPropagation();
              toggleFavorite(r.star!);
              // The row leaves the favorites: keep the keyboard in the picker.
              if (starredRow && view.kind === 'favorites' && !q) {
                input.current?.focus();
              }
            }}
            className={cn(
              '-mr-1 shrink-0 rounded-md p-1 text-muted-foreground opacity-0 group-hover:opacity-100 hover:text-foreground focus-visible:opacity-100 focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none pointer-coarse:opacity-100',
              i === index && 'opacity-100',
              starredRow && 'text-amber-500 opacity-100 hover:text-amber-500',
            )}
          >
            <Star
              className={cn('size-4', starredRow && 'fill-current')}
              aria-hidden
            />
          </button>
        )}
      </li>
    );
  };

  const isThreadScope = harnessSwitch?.scope === 'thread';
  // What the list says before the rows of another harness.
  const intro = !q && viewed && viewed.id !== current.id && harnessSwitch && (
    <li
      role="presentation"
      className="grid gap-1 px-2.5 pt-1.5 pb-2 text-xs text-muted-foreground"
    >
      <span>
        {isThreadScope
          ? `A model here switches this thread to ${viewed.name}.`
          : `A model here switches ${harnessSwitch.agentName} to ${viewed.name} (all threads).`}
        {!viewed.catalog?.models.length &&
          ` Its models show once ${harnessSwitch.agentName} has run on ${viewed.name}.`}
      </span>
      {loginProblem(viewed) && (
        <span className="flex items-start gap-1.5 text-amber-600 dark:text-amber-400">
          <TriangleAlert className="mt-0.5 size-3 shrink-0" aria-hidden />
          {loginProblem(viewed)}
        </span>
      )}
    </li>
  );

  // The list takes the room the page has above (or below) the trigger, and
  // the popover moves left when it would reach past the right edge.
  const [box, setBox] = useState({ height: 320, shift: 0 });
  useLayoutEffect(() => {
    const p = panel.current;
    const b = body.current;
    const host = p?.parentElement;
    if (!p || !b || !host) return;
    const r = host.getBoundingClientRect();
    const room =
      side === 'top' ? r.top - 64 : window.innerHeight - r.bottom - 16;
    const chrome = p.offsetHeight - b.offsetHeight;
    const height = Math.min(320, Math.max(160, room - chrome - 8));
    const vw = document.documentElement.clientWidth || window.innerWidth;
    const shift = Math.max(
      Math.min(0, vw - 16 - (r.left + p.offsetWidth)),
      16 - r.left,
    );
    setBox((x) =>
      x.height === height && x.shift === shift ? x : { height, shift },
    );
  }, [side, pending, value.model]);

  const shown = modelOptionsFor(catalog, value.model);
  const toggles = shown?.options.filter((o) => o.kind === 'toggle') ?? [];
  const pendingTarget = pending && harnessOf(pending.harness);
  const tabs: { view: View; key: string }[] = [
    { view: { kind: 'favorites' }, key: 'favorites' },
    ...rail.map((h) => ({
      view: { kind: 'harness', id: h.id } as View,
      key: `h:${h.id}`,
    })),
  ];
  const viewKey =
    view.kind === 'favorites' ? 'favorites' : `h:${viewed?.id ?? ''}`;
  const onRailKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = tabs.findIndex((t) => t.key === viewKey);
    let next: number;
    if (e.key === 'ArrowDown') next = (i + 1) % tabs.length;
    else if (e.key === 'ArrowUp') next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    else return;
    e.preventDefault();
    chooseView(tabs[next].view);
    e.currentTarget
      .querySelector<HTMLButtonElement>(`[data-tab="${tabs[next].key}"]`)
      ?.focus();
  };

  return (
    <div
      ref={panel}
      role="dialog"
      aria-label={`Choose ${label.toLowerCase()}`}
      onKeyDown={(e) => {
        if (e.key !== 'Escape') return;
        e.preventDefault();
        e.stopPropagation();
        // The question about switching goes first.
        if (pending) cancelPending();
        else onClose();
      }}
      style={
        {
          left: box.shift,
          '--picker-body': `${box.height}px`,
        } as CSSProperties
      }
      className={cn(
        'absolute z-40 flex w-[min(42rem,calc(100vw-2rem))] flex-col overflow-hidden rounded-xl border bg-card text-sm text-foreground shadow-xl shadow-black/30',
        side === 'top' ? 'bottom-full mb-2' : 'top-full mt-1',
      )}
    >
      <div className="flex min-h-0 flex-col sm:flex-row">
        <div
          ref={body}
          className="flex min-w-0 flex-1"
          style={{ height: box.height }}
        >
          <div
            role="tablist"
            aria-label="Model lists"
            aria-orientation="vertical"
            onKeyDown={onRailKey}
            className="flex w-12 shrink-0 flex-col overflow-y-auto border-r py-1"
          >
            <RailTab
              tabKey="favorites"
              label="Favorites"
              selected={viewKey === 'favorites'}
              controls={listId}
              onSelect={() => chooseView({ kind: 'favorites' })}
            >
              <Star
                className={cn(
                  'size-4',
                  starred.length > 0 && 'fill-current text-amber-500',
                )}
                aria-hidden
              />
            </RailTab>
            <div aria-hidden className="mx-auto my-1 h-px w-6 bg-border" />
            {rail.map((h) => (
              <RailTab
                key={h.id}
                tabKey={`h:${h.id}`}
                label={h.name || 'Models'}
                warning={h.id === current.id ? undefined : loginProblem(h)}
                selected={viewKey === `h:${h.id}`}
                controls={listId}
                onSelect={() => chooseView({ kind: 'harness', id: h.id })}
              >
                <HarnessIcon harness={h.id} iconUrl={h.iconUrl} />
              </RailTab>
            ))}
          </div>
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="relative flex shrink-0 items-center gap-2 border-b px-3 after:absolute after:inset-x-0 after:-bottom-px after:h-px after:origin-left after:scale-x-0 after:bg-primary after:transition-transform focus-within:after:scale-x-100 motion-reduce:after:transition-none">
              <Search
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <input
                ref={input}
                autoFocus
                role="combobox"
                aria-label="Search models"
                aria-expanded="true"
                aria-controls={listId}
                aria-activedescendant={index >= 0 ? optionId(index) : undefined}
                aria-autocomplete="list"
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setActiveKey(undefined);
                }}
                onKeyDown={onKeyDown}
                placeholder={
                  allowCustom && catalog?.customModels
                    ? 'Search or type provider/model…'
                    : 'Search models…'
                }
                className="h-11 w-full bg-transparent text-sm placeholder:text-muted-foreground focus:outline-none"
              />
            </div>
            <ul
              id={listId}
              role="listbox"
              aria-label="Models"
              className="min-h-0 flex-1 overflow-y-auto p-1.5"
            >
              {intro}
              {sections.map((s) => (
                <li key={s.key} role="presentation">
                  <ul role="group" aria-label={s.label ?? 'Models'}>
                    {s.label && (
                      <li
                        role="presentation"
                        className="flex items-center gap-1.5 px-2.5 pt-2.5 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase"
                      >
                        {s.harness && (
                          <HarnessIcon
                            harness={s.harness}
                            iconUrl={harnessOf(s.harness)?.iconUrl}
                            className="size-3"
                          />
                        )}
                        {s.label}
                        {q && s.harness !== current.id && harnessSwitch && (
                          <span className="tracking-normal normal-case">
                            · switches{' '}
                            {isThreadScope
                              ? 'this thread'
                              : harnessSwitch.agentName}
                          </span>
                        )}
                      </li>
                    )}
                    {s.rows.map(renderRow)}
                  </ul>
                </li>
              ))}
              {rows.length === 0 && (
                <li
                  role="presentation"
                  className="px-2.5 py-3 text-muted-foreground"
                >
                  {q
                    ? 'No model matches.'
                    : view.kind === 'favorites'
                      ? 'No favorites yet: star a model to keep it here.'
                      : 'No models.'}
                </li>
              )}
            </ul>
          </div>
        </div>
        <aside className="border-t sm:max-h-[var(--picker-body)] sm:w-52 sm:shrink-0 sm:overflow-y-auto sm:border-t-0 sm:border-l">
          {pending && harnessSwitch ? (
            <SwitchConfirm
              key={`${pending.harness}:${pending.model}`}
              sw={harnessSwitch}
              target={pendingTarget}
              model={pending.model}
              onCancel={cancelPending}
              onDone={onClose}
            />
          ) : (
            <Reasoning
              model={value.model}
              modelLabel={
                value.model ? modelName(catalog, value.model) : 'Default'
              }
              shown={shown}
              value={value}
              onSet={setOption}
            />
          )}
        </aside>
      </div>
      {(toggles.length > 0 || multiSelect) && (
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t px-3 py-2">
          {toggles.length > 0 && (
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              {toggles.map((o) => {
                const [off, on] = toggleLabels(o);
                return (
                  <Segmented
                    key={o.id}
                    label={off === 'Standard' ? 'Speed' : o.name}
                    ariaLabel={o.name}
                    hint={o.description}
                    values={[
                      { value: o.values[0].value, name: off },
                      { value: onValue(o), name: on },
                    ]}
                    // Nothing chosen is the harness's default: off.
                    selected={value.options[o.id] ?? o.values[0].value}
                    onSelect={(v) => setOption(o, v)}
                  />
                );
              })}
            </div>
          )}
          {multiSelect && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              {selectedModels && selectedModels.length > 1 ? (
                <>
                  <span className="font-medium text-foreground">
                    {selectedModels.length} models selected (parallel threads)
                  </span>
                  <button
                    type="button"
                    onClick={() => onSelectedModelsChange?.([])}
                    className="rounded px-1.5 py-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
                  >
                    Clear
                  </button>
                </>
              ) : (
                <span>Shift-click to select multiple models</span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function RailTab({
  tabKey,
  label,
  warning,
  selected,
  controls,
  onSelect,
  children,
}: {
  tabKey: string;
  label: string;
  warning?: string;
  selected: boolean;
  controls: string;
  onSelect: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      data-tab={tabKey}
      aria-label={label}
      aria-description={warning}
      aria-selected={selected}
      aria-controls={controls}
      tabIndex={selected ? 0 : -1}
      title={warning ? `${label}: ${warning}` : label}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onSelect}
      className={cn(
        'group relative flex h-11 w-full shrink-0 items-center justify-center text-muted-foreground hover:text-foreground focus-visible:outline-none',
        selected &&
          'text-foreground after:absolute after:inset-y-2 after:right-0 after:w-0.5 after:rounded-l-full after:bg-primary',
      )}
    >
      <span
        className={cn(
          'relative flex size-9 items-center justify-center rounded-lg group-hover:bg-accent group-focus-visible:ring-1 group-focus-visible:ring-ring',
          selected && 'bg-accent',
        )}
      >
        {children}
        {warning && (
          <span
            aria-hidden
            className="absolute top-1 right-1 size-1.5 rounded-full bg-amber-500"
          />
        )}
      </span>
    </button>
  );
}

/**
 * The question before a harness switch, in place of the reasoning column:
 * the harness is the agent's, so it applies to all its threads, and the model
 * starts over.
 */
function SwitchConfirm({
  sw,
  target,
  model,
  onCancel,
  onDone,
}: {
  sw: HarnessSwitch;
  target: HarnessChoice | undefined;
  model: string;
  onCancel: () => void;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  if (!target) return null;
  const modelLabel = model
    ? modelName(target.catalog, model)
    : `${target.name}'s default model`;
  const problem = loginProblem(target);
  const go = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await sw.onSwitch(target.id, model);
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not switch the harness');
      setBusy(false);
    }
  };
  return (
    <div
      role="group"
      aria-label={`Switch to ${target.name}`}
      className="grid gap-2 p-3"
    >
      <p className="flex items-center gap-2 font-medium">
        <HarnessIcon harness={target.id} iconUrl={target.iconUrl} />
        <span>
          {sw.scope === 'thread'
            ? `Switches this thread to ${target.name}`
            : `Switches ${sw.agentName} to ${target.name} (all threads)`}
        </span>
      </p>
      <p className="text-xs text-muted-foreground">
        Model: {modelLabel}. {target.name} starts on the next message, carrying
        your conversation forward via automatic context handoff.
      </p>
      {sw.busy && (
        <p role="alert" className="text-xs text-amber-600 dark:text-amber-400">
          {sw.scope === 'thread'
            ? 'This thread is still running. Switch once it has finished (or stop it).'
            : 'A thread is still running. Switch once it has finished (or stop it): the harness restarts.'}
        </p>
      )}
      {problem && (
        <p className="flex items-start gap-1.5 text-xs text-amber-600 dark:text-amber-400">
          <TriangleAlert className="mt-0.5 size-3 shrink-0" aria-hidden />
          <span>
            {problem} {sw.loginLink} Or sign in inside the sandbox after
            switching.
          </span>
        </p>
      )}
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" disabled={busy || sw.busy} onClick={go}>
          {busy && <Loader2 className="animate-spin" aria-hidden />}
          {problem ? 'Switch anyway' : `Switch to ${target.name}`}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

/** The reasoning effort levels of the current model. */
function Reasoning({
  model,
  modelLabel,
  shown,
  value,
  onSet,
}: {
  model: string;
  modelLabel: string;
  shown: ReturnType<typeof modelOptionsFor>;
  value: Choice;
  onSet: (o: ModelOption, v: string) => void;
}) {
  const efforts = shown?.options.filter((o) => o.kind === 'effort') ?? [];
  const toggles = shown?.options.filter((o) => o.kind === 'toggle') ?? [];
  const hint = (text: string) => (
    <p className="text-xs text-muted-foreground">{text}</p>
  );
  return (
    <div className="grid gap-2 p-3">
      <div className="flex items-baseline justify-between gap-2">
        <p className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Reasoning
        </p>
        <span className="min-w-0 truncate text-xs text-muted-foreground">
          {modelLabel}
        </span>
      </div>
      {!model
        ? hint(
            'Uses the default model and its settings. Pick a model to set its reasoning.',
          )
        : !shown
          ? hint(
              'The reasoning options of this model show here once the agent has run.',
            )
          : efforts.length === 0
            ? hint(
                toggles.length > 0
                  ? 'This model has no effort levels.'
                  : 'This model has no reasoning options.',
              )
            : efforts.map((o) => (
                <Levels
                  key={o.id}
                  label={efforts.length > 1 ? o.name : undefined}
                  ariaLabel={efforts.length > 1 ? o.name : 'Effort'}
                  hint={o.description}
                  values={o.values.map((v) => ({
                    value: v.value,
                    name: valueLabel(o, v),
                    description: v.description,
                  }))}
                  selected={value.options[o.id]}
                  onSelect={(v) => onSet(o, v)}
                />
              ))}
      {shown?.provisional &&
        efforts.length > 0 &&
        hint(
          'Levels as the harness reports them for its other models; it confirms them once the agent has used this model.',
        )}
    </div>
  );
}

interface Value {
  value: string;
  name: string;
  description?: string;
}

/**
 * Arrow keys for a radio group: move to the next or previous choice and
 * choose it, as native radios do.
 */
function radioKeys(values: Value[], onSelect: (v: string) => void) {
  return (e: KeyboardEvent<HTMLDivElement>) => {
    const radios = [
      ...e.currentTarget.querySelectorAll<HTMLButtonElement>('[role=radio]'),
    ];
    const i = radios.indexOf(e.target as HTMLButtonElement);
    if (i < 0) return;
    let next: number;
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight')
      next = (i + 1) % radios.length;
    else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft')
      next = (i - 1 + radios.length) % radios.length;
    else return;
    e.preventDefault();
    radios[next].focus();
    onSelect(values[next].value);
  };
}

/** Effort levels: a column of rows (a row of chips on a phone). */
function Levels({
  label,
  ariaLabel,
  hint,
  values,
  selected,
  onSelect,
}: {
  label?: string;
  ariaLabel: string;
  hint?: string;
  values: Value[];
  selected: string | undefined;
  onSelect: (v: string) => void;
}) {
  const cur = values.findIndex((v) => v.value === selected);
  return (
    <div className="grid gap-1">
      {label && (
        <span className="text-xs text-muted-foreground" title={hint}>
          {label}
        </span>
      )}
      <div
        role="radiogroup"
        aria-label={ariaLabel}
        title={label ? undefined : hint}
        onKeyDown={radioKeys(values, onSelect)}
        className="flex flex-wrap gap-1 sm:flex-col sm:gap-0.5"
      >
        {values.map((v, i) => {
          const on = i === cur;
          return (
            <button
              key={v.value}
              type="button"
              role="radio"
              aria-checked={on}
              tabIndex={(cur < 0 ? i === 0 : on) ? 0 : -1}
              title={v.description}
              onClick={() => onSelect(v.value)}
              className={cn(
                'flex h-9 items-center gap-2 rounded-lg border px-2.5 text-sm hover:bg-accent focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none sm:w-full sm:justify-between sm:border-transparent',
                on && 'border-foreground/30 bg-accent font-medium',
              )}
            >
              <span className="truncate">{v.name}</span>
              <Check
                className={cn(
                  'size-4 shrink-0',
                  !on && 'hidden sm:invisible sm:block',
                )}
                aria-hidden
              />
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** A two-way switch in the footer (fast mode: Standard or Fast). */
function Segmented({
  label,
  ariaLabel,
  hint,
  values,
  selected,
  onSelect,
}: {
  label: string;
  ariaLabel?: string;
  hint?: string;
  values: Value[];
  selected: string | undefined;
  onSelect: (v: string) => void;
}) {
  const cur = values.findIndex((v) => v.value === selected);
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs text-muted-foreground" title={hint}>
        {label}
      </span>
      <div
        role="radiogroup"
        aria-label={ariaLabel ?? label}
        onKeyDown={radioKeys(values, onSelect)}
        className="flex rounded-lg border p-0.5"
      >
        {values.map((v, i) => (
          <button
            key={v.value}
            type="button"
            role="radio"
            aria-checked={i === cur}
            tabIndex={(cur < 0 ? i === 0 : i === cur) ? 0 : -1}
            title={v.description}
            onClick={() => onSelect(v.value)}
            className={cn(
              'h-7 rounded-md px-2.5 text-xs text-muted-foreground hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none',
              i === cur && 'bg-accent font-medium text-foreground',
            )}
          >
            {v.name}
          </button>
        ))}
      </div>
    </div>
  );
}

const TIP_WIDTH = 256;

/**
 * An info icon with the description in a small popover beside it. Fixed
 * positioning keeps it from being clipped by the scrolling list; the option
 * itself carries the description for screen readers.
 */
function InfoTip({ text, label }: { text: string; label: string }) {
  const tipId = useId();
  const btn = useRef<HTMLButtonElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const show = () => {
    const r = btn.current?.getBoundingClientRect();
    if (!r) return;
    const fitsRight = r.right + 8 + TIP_WIDTH <= window.innerWidth;
    setPos({
      left: fitsRight ? r.right + 8 : Math.max(8, r.left - 8 - TIP_WIDTH),
      top: Math.max(8, r.top - 4),
    });
  };
  const hide = () => setPos(null);
  return (
    <>
      <button
        ref={btn}
        type="button"
        tabIndex={-1}
        aria-label={`About ${label}`}
        aria-describedby={pos ? tipId : undefined}
        onMouseEnter={show}
        onMouseLeave={hide}
        onMouseDown={(e) => e.preventDefault()}
        onClick={(e) => {
          e.stopPropagation();
          if (pos) hide();
          else show();
        }}
        className="shrink-0 rounded-full p-0.5 text-muted-foreground hover:text-foreground"
      >
        <Info className="size-3.5" aria-hidden />
      </button>
      {pos && (
        <span
          id={tipId}
          role="tooltip"
          style={{
            position: 'fixed',
            left: pos.left,
            top: pos.top,
            width: TIP_WIDTH,
          }}
          className="z-50 rounded-lg border bg-card p-2 text-xs leading-snug text-foreground shadow-lg shadow-black/30"
        >
          {text}
        </span>
      )}
    </>
  );
}
