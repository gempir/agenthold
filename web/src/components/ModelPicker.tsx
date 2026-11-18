import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown, Search } from 'lucide-react';
import {
  choiceLabel,
  groupModels,
  isCustom,
  keepOptions,
  onValue,
  optionsOf,
  type Choice,
  type ModelCatalog,
  type ModelOption,
} from '@/lib/models';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

type Entry =
  | { kind: 'default' }
  | { kind: 'model'; id: string; name: string; description?: string }
  | { kind: 'custom'; id: string };

const entryModel = (e: Entry) => (e.kind === 'default' ? '' : e.id);

/**
 * The model picker: a compact trigger ("Opus 5.5 · Medium") that opens a
 * popover with a search box, the harness's models grouped by provider, and
 * the options of the highlighted model (reasoning effort, fast mode) on the
 * right. Arrows move, Enter picks, Escape closes, Alt+1…9 picks the n-th
 * model, ←/→ change the effort while the search box is empty.
 */
export function ModelPicker({
  catalog,
  value,
  onChange,
  defaultLabel,
  defaultDescription,
  disabled,
  side = 'top',
  label = 'Model',
  note,
  className,
}: {
  catalog: ModelCatalog | undefined;
  value: Choice;
  onChange: (c: Choice) => void;
  /** The entry for model '' ("Agent default", "Harness default"). */
  defaultLabel: string;
  defaultDescription?: string;
  disabled?: boolean;
  side?: 'top' | 'bottom';
  /** Accessible name of the trigger. */
  label?: string;
  /** A line under the list (when a change takes effect). */
  note?: string;
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

  return (
    <div ref={root} className={cn('relative min-w-0', className)}>
      <button
        ref={trigger}
        type="button"
        aria-label={`${label}: ${choiceLabel(catalog, value, defaultLabel)}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
        className="flex max-w-full min-w-0 items-center gap-1 rounded-md px-1.5 py-1 text-left hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-60"
      >
        <span className="truncate">
          {choiceLabel(catalog, value, defaultLabel)}
        </span>
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
          side={side}
          label={label}
          note={note}
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
  side,
  label,
  note,
}: {
  catalog: ModelCatalog | undefined;
  value: Choice;
  onChange: (c: Choice) => void;
  onClose: () => void;
  defaultLabel: string;
  defaultDescription?: string;
  side: 'top' | 'bottom';
  label: string;
  note?: string;
}) {
  const id = useId();
  const now = useNow(60_000);
  const [query, setQuery] = useState('');
  const groups = useMemo(
    () => groupModels(catalog, query, value.model),
    [catalog, query, value.model],
  );

  // The flat list the keyboard moves through.
  const entries: Entry[] = [];
  const q = query.trim();
  if (!q || defaultLabel.toLowerCase().includes(q.toLowerCase())) {
    entries.push({ kind: 'default' });
  }
  for (const g of groups) {
    for (const m of g.models) {
      entries.push({
        kind: 'model',
        id: m.id,
        name: m.name,
        description: m.description,
      });
    }
  }
  const custom =
    !!catalog?.customModels &&
    !!q &&
    !/\s/.test(q) &&
    !entries.some((e) => e.kind === 'model' && e.id === q);
  if (custom) entries.push({ kind: 'custom', id: q });

  const currentIndex = Math.max(
    0,
    entries.findIndex((e) => entryModel(e) === value.model),
  );
  const [active, setActive] = useState(currentIndex);
  const index = Math.min(active, entries.length - 1);
  const activeEntry = index >= 0 ? entries[index] : undefined;
  const activeModel = activeEntry ? entryModel(activeEntry) : value.model;

  const pick = (model: string) => {
    onChange({ model, options: keepOptions(catalog, model, value.options) });
    onClose();
  };
  const setOption = (model: string, o: ModelOption, v: string) => {
    const base =
      model === value.model
        ? value.options
        : keepOptions(catalog, model, value.options);
    onChange({ model, options: { ...base, [o.id]: v } });
  };

  const optionId = (i: number) => `${id}-opt-${i}`;
  useEffect(() => {
    document
      .getElementById(optionId(index))
      ?.scrollIntoView?.({ block: 'nearest' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index]);

  const effortOf = (model: string) =>
    optionsOf(catalog, model)?.find((o) => o.kind === 'effort');

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const digit = /^Digit([1-9])$/.exec(e.code);
    if (e.altKey && digit) {
      const n = Number(digit[1]) - 1;
      const models = entries.filter((x) => x.kind === 'model');
      if (models[n]) {
        e.preventDefault();
        pick(entryModel(models[n]));
      }
      return;
    }
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        setActive((index + 1) % Math.max(entries.length, 1));
        break;
      case 'ArrowUp':
        e.preventDefault();
        setActive((index - 1 + entries.length) % Math.max(entries.length, 1));
        break;
      case 'Home':
        if (!q) {
          e.preventDefault();
          setActive(0);
        }
        break;
      case 'End':
        if (!q) {
          e.preventDefault();
          setActive(entries.length - 1);
        }
        break;
      case 'Enter':
        e.preventDefault();
        if (activeEntry) pick(entryModel(activeEntry));
        break;
      case 'Escape':
        e.preventDefault();
        e.stopPropagation();
        onClose();
        break;
      case 'ArrowLeft':
      case 'ArrowRight': {
        if (q || !activeModel) break;
        const o = effortOf(activeModel);
        if (!o || o.values.length === 0) break;
        e.preventDefault();
        const cur = (activeModel === value.model && value.options[o.id]) || '';
        let i = o.values.findIndex((x) => x.value === cur);
        if (i < 0) i = e.key === 'ArrowRight' ? -1 : o.values.length;
        i += e.key === 'ArrowRight' ? 1 : -1;
        i = Math.min(Math.max(i, 0), o.values.length - 1);
        setOption(activeModel, o, o.values[i].value);
        break;
      }
    }
  };

  const modelEntries = entries.filter((x) => x.kind === 'model');
  const renderEntry = (e: Entry, title: string, sub?: string, tag?: string) => {
    const model = entryModel(e);
    const i = entries.findIndex(
      (x) => x.kind === e.kind && entryModel(x) === model,
    );
    const pos = modelEntries.findIndex((x) => entryModel(x) === model);
    const n = e.kind === 'model' && pos >= 0 && pos < 9 ? pos + 1 : undefined;
    const current = model === value.model;
    return (
      <li
        key={`${e.kind}-${model}`}
        id={optionId(i)}
        role="option"
        aria-selected={i === index}
        data-current={current || undefined}
        onMouseMove={() => i !== index && setActive(i)}
        onMouseDown={(ev) => ev.preventDefault()}
        onClick={() => pick(model)}
        className={cn(
          'flex cursor-pointer items-start gap-2 rounded-lg px-2 py-1.5 text-sm',
          i === index && 'bg-accent',
        )}
      >
        <Check
          className={cn('mt-0.5 size-3.5 shrink-0', !current && 'invisible')}
          aria-hidden
        />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="truncate">{title}</span>
            {tag && (
              <span className="rounded bg-muted px-1 text-[10px] text-muted-foreground uppercase">
                {tag}
              </span>
            )}
          </span>
          {sub && (
            <span className="block truncate text-xs text-muted-foreground">
              {sub}
            </span>
          )}
        </span>
        {n !== undefined && (
          <kbd
            className="mt-0.5 shrink-0 font-sans text-[10px] text-muted-foreground"
            title={`Alt+${n}`}
          >
            ⌥{n}
          </kbd>
        )}
      </li>
    );
  };

  const shown = activeModel ? optionsOf(catalog, activeModel) : undefined;

  return (
    <div
      role="dialog"
      aria-label={`Choose ${label.toLowerCase()}`}
      className={cn(
        'absolute left-0 z-40 flex w-[min(36rem,calc(100vw-2rem))] flex-col overflow-hidden rounded-xl border bg-card text-sm text-foreground shadow-xl shadow-black/30 sm:flex-row',
        side === 'top' ? 'bottom-full mb-2' : 'top-full mt-1',
      )}
    >
      <div className="flex min-w-0 flex-1 flex-col sm:border-r">
        <div className="flex items-center gap-2 border-b px-3">
          <Search
            className="size-3.5 shrink-0 text-muted-foreground"
            aria-hidden
          />
          <input
            autoFocus
            role="combobox"
            aria-label="Search models"
            aria-expanded="true"
            aria-controls={`${id}-list`}
            aria-activedescendant={index >= 0 ? optionId(index) : undefined}
            aria-autocomplete="list"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActive(0);
            }}
            onKeyDown={onKeyDown}
            placeholder={
              catalog?.customModels
                ? 'Search or type provider/model'
                : 'Search models'
            }
            className="h-10 w-full bg-transparent text-sm placeholder:text-muted-foreground focus:outline-none"
          />
        </div>
        <ul
          id={`${id}-list`}
          role="listbox"
          aria-label="Models"
          className="max-h-72 overflow-y-auto p-1"
        >
          {entries[0]?.kind === 'default' &&
            renderEntry(entries[0], defaultLabel, defaultDescription)}
          {groups.map((g) => (
            <li key={`g-${g.name}`} role="presentation">
              <ul role="group" aria-label={g.name || 'Models'}>
                {g.name && (
                  <li
                    role="presentation"
                    className="px-2 pt-2 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase"
                  >
                    {g.name}
                  </li>
                )}
                {g.models.map((m) =>
                  renderEntry(
                    {
                      kind: 'model',
                      id: m.id,
                      name: m.name,
                      description: m.description,
                    },
                    m.name,
                    m.description ?? (m.name !== m.id ? m.id : undefined),
                    isCustom(catalog, m.id) ? 'custom' : undefined,
                  ),
                )}
              </ul>
            </li>
          ))}
          {custom &&
            renderEntry(
              { kind: 'custom', id: q },
              `Use “${q}”`,
              'A model id the harness does not list',
            )}
          {entries.length === 0 && (
            <li role="presentation" className="px-2 py-3 text-muted-foreground">
              No model matches.
            </li>
          )}
        </ul>
        <p className="border-t px-3 py-2 text-xs text-muted-foreground">
          {catalog?.source === 'harness' && catalog.seenAt
            ? `As ${catalog.harness} reported them ${relTime(catalog.seenAt, now) === 'now' ? 'just now' : `${relTime(catalog.seenAt, now)} ago`}.`
            : 'Built-in list: the harness reports its own models once the agent has run.'}
          {note && <> {note}</>}
        </p>
      </div>
      <OptionsColumn
        model={activeModel}
        name={
          activeEntry?.kind === 'model'
            ? activeEntry.name
            : activeEntry?.kind === 'custom'
              ? activeEntry.id
              : defaultLabel
        }
        options={shown}
        value={value}
        onSet={setOption}
      />
    </div>
  );
}

function OptionsColumn({
  model,
  name,
  options,
  value,
  onSet,
}: {
  model: string;
  name: string;
  options: ModelOption[] | undefined;
  value: Choice;
  onSet: (model: string, o: ModelOption, v: string) => void;
}) {
  const selected = (o: ModelOption) =>
    model === value.model ? value.options[o.id] : undefined;
  return (
    <div className="grid content-start gap-3 border-t p-3 sm:w-52 sm:border-t-0">
      <p className="truncate text-xs font-medium text-muted-foreground">
        {name}
      </p>
      {!model ? (
        <p className="text-xs text-muted-foreground">
          Uses the default model and its settings.
        </p>
      ) : options === undefined ? (
        <p className="text-xs text-muted-foreground">
          Its options show here once the agent has used this model.
        </p>
      ) : options.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No options for this model.
        </p>
      ) : (
        options.map((o) =>
          o.kind === 'toggle' ? (
            <div
              key={o.id}
              className="flex items-center justify-between gap-2 text-sm"
            >
              <span title={o.description}>{o.name}</span>
              <button
                type="button"
                role="switch"
                aria-label={o.name}
                aria-checked={selected(o) === onValue(o)}
                onClick={() =>
                  onSet(
                    model,
                    o,
                    selected(o) === onValue(o) ? o.values[0].value : onValue(o),
                  )
                }
                className={cn(
                  'relative h-5 w-9 shrink-0 rounded-full transition-colors',
                  selected(o) === onValue(o) ? 'bg-foreground' : 'bg-muted',
                )}
              >
                <span
                  className={cn(
                    'absolute top-0.5 left-0.5 size-4 rounded-full bg-background transition-transform',
                    selected(o) === onValue(o) && 'translate-x-4',
                  )}
                />
              </button>
            </div>
          ) : (
            <div key={o.id} className="grid gap-1.5">
              <span
                className="text-xs text-muted-foreground"
                title={o.description}
              >
                {o.name}
              </span>
              <div
                role="radiogroup"
                aria-label={o.name}
                className="flex flex-wrap gap-1"
              >
                {o.values.map((v) => (
                  <button
                    key={v.value}
                    type="button"
                    role="radio"
                    aria-checked={selected(o) === v.value}
                    title={v.description}
                    onClick={() => onSet(model, o, v.value)}
                    className={cn(
                      'rounded-md border px-2 py-0.5 text-xs hover:bg-accent',
                      selected(o) === v.value && 'border-foreground bg-accent',
                    )}
                  >
                    {v.name}
                  </button>
                ))}
              </div>
            </div>
          ),
        )
      )}
    </div>
  );
}
