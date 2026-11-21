import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import {
  Check,
  ChevronDown,
  ChevronRight,
  Info,
  Loader2,
  Search,
  TriangleAlert,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  choiceLabel,
  groupModels,
  isCustom,
  keepOptions,
  modelName,
  modelOptionsFor,
  onValue,
  toggleLabels,
  valueLabel,
  type Choice,
  type ModelCatalog,
  type ModelGroup,
  type ModelOption,
} from '@/lib/models';
import { relTime, useNow } from '@/lib/time';
import { cn } from '@/lib/utils';

/** A harness the agent can be switched to, with what is known of its models. */
export interface HarnessChoice {
  id: string;
  name: string;
  /** Its catalog: what it reported, or the built-in list; undefined while unknown. */
  catalog?: ModelCatalog;
}

/**
 * Lets the picker switch the agent's harness: the other harnesses are listed
 * under the current one, and picking one of their models asks to confirm.
 * The harness belongs to the agent, so the switch applies to all its chats.
 */
export interface HarnessSwitch {
  agentName: string;
  others: HarnessChoice[];
  /** Does the switch (rejects with the reason it could not). */
  onSwitch: (harness: string, model: string) => Promise<unknown>;
  /** A conversation is running: the harness cannot be stopped now. */
  busy?: boolean;
  /** Where to connect a login, shown with a missing-login warning. */
  loginLink?: ReactNode;
}

type Entry =
  | { kind: 'default' }
  | { kind: 'model'; id: string; name: string }
  | { kind: 'custom'; id: string }
  /** The heading row of another harness; opens and closes its models. */
  | { kind: 'harness'; harness: string }
  /** A model of another harness ('' for its default). */
  | { kind: 'other'; harness: string; id: string };

const entryKey = (e: Entry) => {
  switch (e.kind) {
    case 'default':
      return 'default';
    case 'harness':
      return `h:${e.harness}`;
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

/** What the picker lists of another harness for the query. */
interface OtherView {
  h: HarnessChoice;
  /** No models known: one row stands for the whole harness. */
  single: boolean;
  /** The heading toggles (no search going on, models known). */
  toggles: boolean;
  open: boolean;
  defaultRow: boolean;
  groups: ModelGroup[];
}

const words = (q: string) => q.toLowerCase().split(/\s+/).filter(Boolean);

function otherView(h: HarnessChoice, query: string, open: boolean): OtherView {
  const w = words(query);
  const hay = `${h.name} agent default`.toLowerCase();
  const nameMatch = w.every((x) => h.name.toLowerCase().includes(x));
  const groups = groupModels(h.catalog, nameMatch ? '' : query, '');
  const count = h.catalog?.models.length ?? 0;
  const single = count === 0;
  const defaultRow = w.every((x) => hay.includes(x));
  return {
    h,
    single,
    toggles: !single && w.length === 0,
    open: w.length > 0 || open,
    defaultRow,
    groups,
  };
}

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
 * The model picker: a compact trigger ("Opus 5.5 · Medium") that opens a
 * popover with a search box, the harness's models grouped by provider (one
 * line each, the description behind an info icon) and a Reasoning section
 * for the chosen model: effort levels and fast mode. Choices apply at once
 * and the popover stays open, so a model and its reasoning are set in one go.
 * Arrows move, Enter picks, Escape closes, Alt+1…9 picks the n-th model,
 * ←/→ change the effort while the search box is empty.
 *
 * With harnessSwitch the other harnesses follow the current one; picking one
 * of their models asks to switch the agent (all its chats) to that harness.
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
  leading,
  harnessSwitch,
  footer,
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
  /** Shown in the trigger before the choice (the harness). */
  leading?: ReactNode;
  harnessSwitch?: HarnessSwitch;
  /** Under the note (a link to the settings). */
  footer?: ReactNode;
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
        {leading}
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
          harnessSwitch={harnessSwitch}
          footer={footer}
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
  harnessSwitch,
  footer,
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
  harnessSwitch?: HarnessSwitch;
  footer?: ReactNode;
}) {
  const id = useId();
  const now = useNow(60_000);
  const [query, setQuery] = useState('');
  const [expanded, setExpanded] = useState<string[]>([]);
  const [pending, setPending] = useState<{ harness: string; model: string }>();
  const input = useRef<HTMLInputElement>(null);
  // Back to the list, with the search box focused again for the keyboard.
  const cancelPending = () => {
    setPending(undefined);
    input.current?.focus();
  };
  const groups = useMemo(
    () => groupModels(catalog, query, value.model),
    [catalog, query, value.model],
  );
  const others = harnessSwitch?.others;
  const otherViews = useMemo(
    () =>
      (others ?? []).map((h) => otherView(h, query, expanded.includes(h.id))),
    [others, query, expanded],
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
  for (const v of otherViews) {
    const hasRows = v.defaultRow || v.groups.length > 0;
    if (!hasRows) continue;
    if (v.single) {
      entries.push({ kind: 'other', harness: v.h.id, id: '' });
      continue;
    }
    if (v.toggles) entries.push({ kind: 'harness', harness: v.h.id });
    if (!v.open) continue;
    if (v.defaultRow) entries.push({ kind: 'other', harness: v.h.id, id: '' });
    for (const g of v.groups) {
      for (const m of g.models) {
        entries.push({ kind: 'other', harness: v.h.id, id: m.id });
      }
    }
  }

  const currentIndex = Math.max(
    0,
    entries.findIndex(
      (e) => isCurrentEntry(e) && entryModel(e) === value.model,
    ),
  );
  const [active, setActive] = useState(currentIndex);
  const index = Math.min(active, entries.length - 1);
  const activeEntry = index >= 0 ? entries[index] : undefined;

  const pick = (model: string) => {
    setPending(undefined);
    onChange({ model, options: keepOptions(catalog, model, value.options) });
  };
  const toggle = (harness: string) =>
    setExpanded((x) =>
      x.includes(harness) ? x.filter((y) => y !== harness) : [...x, harness],
    );
  const activate = (e: Entry) => {
    switch (e.kind) {
      case 'harness':
        toggle(e.harness);
        break;
      case 'other':
        setPending({ harness: e.harness, model: e.id });
        break;
      default:
        pick(entryModel(e));
    }
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
        if (activeEntry) activate(activeEntry);
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
    detail?: string,
  ) => {
    const key = entryKey(e);
    const i = entries.findIndex((x) => entryKey(x) === key);
    const model = isCurrentEntry(e) ? entryModel(e) : undefined;
    const pos = modelEntries.findIndex((x) => entryKey(x) === key);
    const n = e.kind === 'model' && pos >= 0 && pos < 9 ? pos + 1 : undefined;
    const current = model !== undefined && model === value.model;
    const chosenForSwitch =
      e.kind === 'other' &&
      pending?.harness === e.harness &&
      pending.model === e.id;
    return (
      <li
        key={key}
        id={optionId(i)}
        role="option"
        aria-selected={i === index}
        aria-description={description}
        data-current={current || undefined}
        data-pending={chosenForSwitch || undefined}
        title={e.kind === 'other' ? e.id || undefined : model || undefined}
        onMouseMove={() => i !== index && setActive(i)}
        onMouseDown={(ev) => ev.preventDefault()}
        onClick={() => activate(e)}
        className={cn(
          'flex h-8 cursor-pointer items-center gap-2 rounded-lg px-2 text-sm',
          i === index && 'bg-accent',
        )}
      >
        <Check
          className={cn(
            'size-3.5 shrink-0',
            !current && !chosenForSwitch && 'invisible',
            chosenForSwitch && 'text-sky-500',
          )}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate">{title}</span>
        {detail && (
          <span className="shrink-0 text-xs text-muted-foreground">
            {detail}
          </span>
        )}
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

  const renderGroups = (list: ModelGroup[], harness?: HarnessChoice) =>
    list.map((g) => (
      <li key={`g-${harness?.id ?? ''}-${g.name}`} role="presentation">
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
            harness
              ? renderEntry(
                  { kind: 'other', harness: harness.id, id: m.id },
                  m.name,
                  m.description,
                )
              : renderEntry(
                  { kind: 'model', id: m.id, name: m.name },
                  m.name,
                  m.description,
                  isCustom(catalog, m.id) ? 'custom' : undefined,
                ),
          )}
        </ul>
      </li>
    ));

  const renderOther = (v: OtherView) => {
    if (!v.defaultRow && v.groups.length === 0) return null;
    if (v.single) {
      return (
        <li key={`oh-${v.h.id}`} role="presentation">
          <ul role="group" aria-label={v.h.name}>
            {renderEntry(
              { kind: 'other', harness: v.h.id, id: '' },
              v.h.name,
              `Switches the agent to ${v.h.name} on its own default model`,
              undefined,
              'Agent default',
            )}
          </ul>
        </li>
      );
    }
    const headingKey = entryKey({ kind: 'harness', harness: v.h.id });
    const hi = entries.findIndex((x) => entryKey(x) === headingKey);
    return (
      <li key={`oh-${v.h.id}`} role="presentation">
        <ul role="group" aria-label={v.h.name}>
          {v.toggles ? (
            <li
              id={optionId(hi)}
              role="option"
              aria-selected={hi === index}
              aria-label={`${v.h.name}, ${v.open ? 'hide' : 'show'} models`}
              data-expanded={v.open}
              onMouseMove={() => hi !== index && setActive(hi)}
              onMouseDown={(ev) => ev.preventDefault()}
              onClick={() => toggle(v.h.id)}
              className={cn(
                'flex h-8 cursor-pointer items-center gap-2 rounded-lg px-2 text-sm',
                hi === index && 'bg-accent',
              )}
            >
              <ChevronRight
                className={cn(
                  'size-3.5 shrink-0 text-muted-foreground transition-transform',
                  v.open && 'rotate-90',
                )}
                aria-hidden
              />
              <span className="min-w-0 flex-1 truncate">{v.h.name}</span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {v.h.catalog?.models.length} models
              </span>
            </li>
          ) : (
            <li
              role="presentation"
              className="px-2 pt-2 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase"
            >
              {v.h.name}
            </li>
          )}
          {v.open && (
            <>
              {v.defaultRow &&
                renderEntry(
                  { kind: 'other', harness: v.h.id, id: '' },
                  'Agent default',
                  `Switches the agent to ${v.h.name} on its own default model`,
                )}
              {renderGroups(v.groups, v.h)}
            </>
          )}
        </ul>
      </li>
    );
  };

  const currentHarness = catalog?.harness;
  return (
    <div
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
          ref={input}
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
        {renderGroups(groups)}
        {custom &&
          renderEntry(
            { kind: 'custom', id: q },
            `Use “${q}”`,
            'A model id the harness does not list',
          )}
        {otherViews.length > 0 && (
          <li
            role="presentation"
            className="px-2 pt-3 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase"
          >
            Switch {harnessSwitch?.agentName} to
          </li>
        )}
        {otherViews.map(renderOther)}
        {entries.length === 0 && (
          <li role="presentation" className="px-2 py-3 text-muted-foreground">
            No model matches.
          </li>
        )}
      </ul>
      {pending && harnessSwitch ? (
        <SwitchConfirm
          key={`${pending.harness}:${pending.model}`}
          sw={harnessSwitch}
          target={otherViews.find((v) => v.h.id === pending.harness)?.h}
          model={pending.model}
          onCancel={cancelPending}
          onDone={onClose}
        />
      ) : (
        <Reasoning
          model={value.model}
          shown={shown}
          value={value}
          onSet={setOption}
        />
      )}
      <p className="border-t px-3 py-2 text-xs text-muted-foreground">
        {catalog?.source === 'harness' && catalog.seenAt
          ? `As ${currentHarness} reported them ${relTime(catalog.seenAt, now) === 'now' ? 'just now' : `${relTime(catalog.seenAt, now)} ago`}.`
          : 'Built-in list: the harness reports its own models once the agent has run.'}
        {note && <> {note}</>}
        {footer && <span className="mt-1 block">{footer}</span>}
      </p>
    </div>
  );
}

/**
 * The question before a harness switch, inline under the list: the harness is
 * the agent's, so it applies to all its chats, and the model starts over.
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
      className="grid gap-2 border-t p-3"
    >
      <p className="font-medium">
        Switches {sw.agentName} to {target.name} (all chats)
      </p>
      <p className="text-xs text-muted-foreground">
        Model: {modelLabel}. {target.name} starts with the next message. The
        chats keep their history but begin with a fresh context.
      </p>
      {sw.busy && (
        <p role="alert" className="text-xs text-amber-600 dark:text-amber-400">
          A chat is still running. Switch once it has finished (or stop it): the
          harness restarts.
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
      <div className="flex gap-2">
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
