import { useState } from 'react';
import { structuredPatch } from 'diff';
import {
  ChevronRight,
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

const STATUS_STYLE: Record<string, string> = {
  pending: 'bg-muted text-muted-foreground',
  in_progress: 'bg-blue-500/15 text-blue-600',
  completed: 'bg-emerald-500/15 text-emerald-600',
  failed: 'bg-destructive/15 text-destructive',
};

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
    <div data-testid="diff" className="overflow-x-auto rounded border text-xs">
      <div className="border-b bg-muted px-2 py-1 font-mono">{path}</div>
      {patch.hunks.length === 0 && (
        <div className="px-2 py-1 text-muted-foreground">No changes</div>
      )}
      {patch.hunks.map((h, i) => (
        <pre key={i} className="font-mono leading-snug">
          <div className="bg-muted/50 px-2 text-muted-foreground">
            {`@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@`}
          </div>
          {h.lines.map((l, j) => (
            <div
              key={j}
              className={`px-2 whitespace-pre ${
                l[0] === '+'
                  ? 'bg-emerald-500/15'
                  : l[0] === '-'
                    ? 'bg-destructive/15'
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
      <pre className="max-h-64 overflow-auto rounded bg-muted p-2 text-xs whitespace-pre-wrap">
        {c.content.text}
      </pre>
    );
  return null;
}

export function ToolCard({ tool }: { tool: ToolCall }) {
  const hasDiff = tool.content.some((c) => c.type === 'diff');
  const [open, setOpen] = useState(hasDiff);
  const Icon = ICONS[tool.toolKind] ?? Wrench;
  const hasBody = tool.content.length > 0 || tool.locations.length > 0;
  return (
    <div className="rounded-lg border bg-card text-sm" data-testid="tool-call">
      <button
        type="button"
        aria-expanded={open}
        disabled={!hasBody}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 p-2 text-left"
      >
        <Icon className="size-4 shrink-0" aria-hidden />
        <span className="min-w-0 flex-1 truncate font-medium">
          {tool.title}
        </span>
        <span
          className={`rounded px-1.5 py-0.5 text-xs ${STATUS_STYLE[tool.status] ?? ''}`}
        >
          {tool.status.replace('_', ' ')}
        </span>
        {hasBody && (
          <ChevronRight
            className={`size-4 transition-transform ${open ? 'rotate-90' : ''}`}
            aria-hidden
          />
        )}
      </button>
      {open && hasBody && (
        <div className="grid gap-2 border-t p-2">
          {tool.locations.length > 0 && (
            <ul className="font-mono text-xs text-muted-foreground">
              {tool.locations.map((l, i) => (
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
