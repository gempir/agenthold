import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown, Info, Search } from 'lucide-react';
import {
  choiceLabel,
  groupModels,
  isCustom,
  keepOptions,
  modelOptionsFor,
  onValue,
  toggleLabels,
  valueLabel,
  type Choice,
  type ModelCatalog,
  type ModelOption,
} from '@/lib/models';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

type Entry =
  | { kind: 'default' }
  | { kind: 'model'; id: string; name: string }
  | { kind: 'custom'; id: string };

const entryModel = (e: Entry) => (e.kind === 'default' ? '' : e.id);

/**
 * The model picker: a compact trigger ("Opus 5.5 · Medium") that opens a
 * popover with a search box, the harness's models grouped by provider (one
 * line each, the description behind an info icon) and a Reasoning section
 * for the chosen model: effort levels and fast mode. Choices apply at once
 * and the popover stays open, so a model and its reasoning are set in one go.
 * Arrows move, Enter picks, Escape closes, Alt+1…9 picks the n-th model,
 * ←/→ change the effort while the search box is empty.
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
  /** Offer typed model ids (harnesses that take them); owners only. */
  allowCustom?: boolean;
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
          allowCustom={allowCustom}
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
  allowCustom,
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
  allowCustom: boolean;
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
      entries.push({ kind: 'model', id: m.id, name: m.name });
    }
  }
  const custom =
    allowCustom &&
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

  const pick = (model: string) => {
    onChange({ model, options: keepOptions(catalog, model, value.options) });
  };
  const setOption = (o: ModelOption, v: string) =>
    onChange({ model: value.model, options: { ...value.options, [o.id]: v } });

  const optionId = (i: number) => `${id}-opt-${i}`;
  useEffect(() => {
    document
      .getElementById(optionId(index))
      ?.scrollIntoView?.({ block: 'nearest' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index]);

  const shown = modelOptionsFor(catalog, value.model);
  const effort = shown?.options.find((o) => o.kind === 'effort');

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
        if (q || !effort || effort.values.length === 0) break;
        e.preventDefault();
        const cur = value.options[effort.id] ?? '';
        let i = effort.values.findIndex((x) => x.value === cur);
        if (i < 0) i = e.key === 'ArrowRight' ? -1 : effort.values.length;
        i += e.key === 'ArrowRight' ? 1 : -1;
        i = Math.min(Math.max(i, 0), effort.values.length - 1);
        setOption(effort, effort.values[i].value);
        break;
      }
    }
  };

  const modelEntries = entries.filter((x) => x.kind === 'model');
  const renderEntry = (
    e: Entry,
    title: string,
    description?: string,
    tag?: string,
  ) => {
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
        aria-description={description}
        data-current={current || undefined}
        title={model || undefined}
        onMouseMove={() => i !== index && setActive(i)}
        onMouseDown={(ev) => ev.preventDefault()}
        onClick={() => pick(model)}
        className={cn(
          'flex h-8 cursor-pointer items-center gap-2 rounded-lg px-2 text-sm',
          i === index && 'bg-accent',
        )}
      >
        <Check
          className={cn('size-3.5 shrink-0', !current && 'invisible')}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate">{title}</span>
        {tag && (
          <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground uppercase">
            {tag}
          </span>
        )}
        {description && <InfoTip text={description} label={title} />}
        {n !== undefined && (
          <kbd
            className="shrink-0 font-sans text-[10px] text-muted-foreground"
            title={`Alt+${n}`}
          >
            ⌥{n}
          </kbd>
        )}
      </li>
    );
  };

  return (
    <div
      role="dialog"
      aria-label={`Choose ${label.toLowerCase()}`}
      className={cn(
        'absolute left-0 z-40 flex w-[min(22rem,calc(100vw-2rem))] flex-col rounded-xl border bg-card text-sm text-foreground shadow-xl shadow-black/30',
        side === 'top' ? 'bottom-full mb-2' : 'top-full mt-1',
      )}
    >
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
            allowCustom && catalog?.customModels
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
        className="max-h-64 overflow-y-auto p-1"
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
                  { kind: 'model', id: m.id, name: m.name },
                  m.name,
                  m.description,
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
      <Reasoning
        model={value.model}
        shown={shown}
        value={value}
        onSet={setOption}
      />
      <p className="border-t px-3 py-2 text-xs text-muted-foreground">
        {catalog?.source === 'harness' && catalog.seenAt
          ? `As ${catalog.harness} reported them ${relTime(catalog.seenAt, now) === 'now' ? 'just now' : `${relTime(catalog.seenAt, now)} ago`}.`
          : 'Built-in list: the harness reports its own models once the agent has run.'}
        {note && <> {note}</>}
      </p>
    </div>
  );
}

/** Reasoning effort and fast mode of the chosen model. */
function Reasoning({
  model,
  shown,
  value,
  onSet,
}: {
  model: string;
  shown: ReturnType<typeof modelOptionsFor>;
  value: Choice;
  onSet: (o: ModelOption, v: string) => void;
}) {
  const efforts = shown?.options.filter((o) => o.kind === 'effort') ?? [];
  const toggles = shown?.options.filter((o) => o.kind === 'toggle') ?? [];
  const selected = (o: ModelOption) => value.options[o.id];
  return (
    <div className="grid gap-2.5 border-t p-3">
      <p className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        Reasoning
      </p>
      {!model ? (
        <p className="text-xs text-muted-foreground">
          Uses the default model and its settings. Pick a model to set its
          reasoning.
        </p>
      ) : !shown ? (
        <p className="text-xs text-muted-foreground">
          The reasoning options of this model show here once the agent has run.
        </p>
      ) : efforts.length + toggles.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          This model has no reasoning options.
        </p>
      ) : (
        <>
          {efforts.map((o) => (
            <Segmented
              key={o.id}
              label={efforts.length > 1 ? o.name : 'Effort'}
              hint={o.description}
              values={o.values.map((v) => ({
                value: v.value,
                name: valueLabel(o, v),
                description: v.description,
              }))}
              selected={selected(o)}
              onSelect={(v) => onSet(o, v)}
            />
          ))}
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
                selected={selected(o) ?? o.values[0].value}
                onSelect={(v) => onSet(o, v)}
              />
            );
          })}
          {shown.provisional && (
            <p className="text-xs text-muted-foreground">
              Levels as the harness reports them for its other models; it
              confirms them once the agent has used this model.
            </p>
          )}
        </>
      )}
    </div>
  );
}

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
  values: { value: string; name: string; description?: string }[];
  selected: string | undefined;
  onSelect: (v: string) => void;
}) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-muted-foreground" title={hint}>
        {label}
      </span>
      <div
        role="radiogroup"
        aria-label={ariaLabel ?? label}
        className="flex flex-wrap gap-1"
      >
        {values.map((v) => (
          <button
            key={v.value}
            type="button"
            role="radio"
            aria-checked={selected === v.value}
            title={v.description}
            onClick={() => onSelect(v.value)}
            className={cn(
              'rounded-md border px-2 py-0.5 text-xs hover:bg-accent',
              selected === v.value && 'border-foreground bg-accent',
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
