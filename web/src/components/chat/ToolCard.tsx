import { useState } from 'react';
import { structuredPatch } from 'diff';
import {
  Check,
  ChevronRight,
  Circle,
  Loader2,
  X,
  FileText,
  Globe,
  Lightbulb,
  MoveRight,
  Pencil,
  Search,
  SquareTerminal,
  Trash2,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import type { ToolCall, ToolContent, ToolKind } from '@/lib/session';

const ICONS: Record<ToolKind, LucideIcon> = {
  read: FileText,
  edit: Pencil,
  delete: Trash2,
  move: MoveRight,
  search: Search,
  execute: SquareTerminal,
  think: Lightbulb,
  fetch: Globe,
  other: Wrench,
};

function StatusGlyph({ status }: { status: string }) {
  const label = status.replace('_', ' ');
  if (status === 'in_progress')
    return (
      <Loader2
        role="img"
        aria-label={label}
        className="size-3.5 shrink-0 animate-spin text-sky-400"
      />
    );
  if (status === 'failed')
    return (
      <X
        role="img"
        aria-label={label}
        className="size-3.5 shrink-0 text-destructive"
      />
    );
  if (status === 'completed')
    return (
      <Check
        role="img"
        aria-label={label}
        className="size-3.5 shrink-0 text-muted-foreground/70"
      />
    );
  return (
    <Circle
      role="img"
      aria-label={label}
      className="size-3 shrink-0 text-muted-foreground/60"
    />
  );
}

export function DiffView({
  path,
  oldText,
  newText,
}: {
  path: string;
  oldText?: string | null;
  newText: string;
}) {
  const patch = structuredPatch(path, path, oldText ?? '', newText, '', '', {
    context: 3,
  });
  return (
    <div
      data-testid="diff"
      className="overflow-x-auto rounded-lg border bg-background/40 text-xs"
    >
      <div className="border-b px-3 py-1.5 font-mono text-muted-foreground">
        {path}
      </div>
      {patch.hunks.length === 0 && (
        <div className="px-3 py-1 text-muted-foreground">No changes</div>
      )}
      {patch.hunks.map((h, i) => (
        <pre key={i} className="py-1 font-mono leading-snug">
          <div className="px-3 text-muted-foreground/70">
            {`@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@`}
          </div>
          {h.lines.map((l, j) => (
            <div
              key={j}
              className={`px-3 whitespace-pre ${
                l[0] === '+'
                  ? 'bg-emerald-500/12 text-emerald-700 dark:text-emerald-300'
                  : l[0] === '-'
                    ? 'bg-red-500/12 text-red-700 dark:text-red-300'
                    : ''
              }`}
            >
              {l || ' '}
            </div>
          ))}
        </pre>
      ))}
    </div>
  );
}

function Content({ c }: { c: ToolContent }) {
  if (c.type === 'diff')
    return <DiffView path={c.path} oldText={c.oldText} newText={c.newText} />;
  if (c.type === 'terminal')
    return (
      <p className="text-xs text-muted-foreground">
        Terminal output ({c.terminalId})
      </p>
    );
  if (c.content?.text)
    return (
      <pre className="max-h-64 overflow-auto rounded-lg border bg-background/40 p-3 font-mono text-xs whitespace-pre-wrap text-muted-foreground">
        {c.content.text}
      </pre>
    );
  return null;
}

/** One tool call as a quiet one-line row that expands to its output. */
export function ToolCard({ tool }: { tool: ToolCall }) {
  const hasDiff = tool.content.some((c) => c.type === 'diff');
  const [open, setOpen] = useState(hasDiff);
  const Icon = ICONS[tool.toolKind] ?? Wrench;
  // A diff already names its file; don't list it again as a location.
  const diffPaths = new Set(
    tool.content.flatMap((c) => (c.type === 'diff' ? [c.path] : [])),
  );
  const locations = tool.locations.filter((l) => !diffPaths.has(l.path));
  const hasBody = tool.content.length > 0 || locations.length > 0;
  return (
    <div className="text-sm" data-testid="tool-call">
      <button
        type="button"
        aria-expanded={hasBody ? open : undefined}
        disabled={!hasBody}
        onClick={() => setOpen((o) => !o)}
        className="group flex w-full items-center gap-2 rounded-md py-1 text-left text-muted-foreground enabled:hover:text-foreground"
      >
        <Icon className="size-3.5 shrink-0" aria-hidden />
        <span className="min-w-0 truncate">{tool.title}</span>
        <StatusGlyph status={tool.status} />
        {hasBody && (
          <ChevronRight
            className={`size-3.5 shrink-0 opacity-60 transition-transform group-hover:opacity-100 ${open ? 'rotate-90' : ''}`}
            aria-hidden
          />
        )}
      </button>
      {open && hasBody && (
        <div className="mt-1 mb-2 ml-1.5 grid gap-2 border-l pl-4">
          {locations.length > 0 && (
            <ul className="font-mono text-xs text-muted-foreground">
              {locations.map((l, i) => (
                <li key={i}>
                  {l.path}
                  {l.line != null ? `:${l.line}` : ''}
                </li>
              ))}
            </ul>
          )}
          {tool.content.map((c, i) => (
            <Content key={i} c={c} />
          ))}
        </div>
      )}
    </div>
  );
}
