import {
  Fragment,
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, Pencil, Save, X } from 'lucide-react';
import { api, ApiError, unwrap } from '@/api/client';
import { fileTextQuery, type TextFile } from '@/api/queries';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { CopyButton } from '@/components/workspace/CopyButton';
import {
  AttachedNote,
  CommentBox,
  nextSelection,
  selectionBounds,
  type LineSelection,
} from '@/components/workspace/LineComment';
import { Tokens } from '@/components/workspace/Tokens';
import { attachToComposer, newContextId } from '@/lib/composerContext';
import {
  AGENT_HOME,
  baseName,
  contentUrl,
  dirName,
  extOf,
  formatSize,
  resolveFileRef,
} from '@/lib/files';
import { highlightLines, langOfPath } from '@/lib/highlight';
import { cn } from '@/lib/utils';

const Markdown = lazy(() => import('@/components/chat/Markdown'));

const IMAGE = new Set([
  'png',
  'jpg',
  'jpeg',
  'gif',
  'webp',
  'avif',
  'bmp',
  'ico',
  'svg',
]);

type Kind = 'markdown' | 'html' | 'csv' | 'tsv' | 'image' | 'pdf' | 'text';

export function viewKind(path: string): Kind {
  const ext = extOf(path);
  if (ext === 'md' || ext === 'markdown' || ext === 'mdx') return 'markdown';
  if (ext === 'html' || ext === 'htm') return 'html';
  if (ext === 'csv') return 'csv';
  if (ext === 'tsv') return 'tsv';
  if (ext === 'pdf') return 'pdf';
  if (IMAGE.has(ext)) return 'image';
  return 'text';
}

/** Rows of a CSV or TSV text: quoted fields may hold separators and newlines. */
export function parseDelimited(text: string, sep: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        field += '"';
        i++;
      } else if (c === '"') quoted = false;
      else field += c;
    } else if (c === '"' && field === '') quoted = true;
    else if (c === sep) {
      row.push(field);
      field = '';
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++;
      row.push(field);
      rows.push(row);
      row = [];
      field = '';
    } else field += c;
  }
  if (field || row.length) {
    row.push(field);
    rows.push(row);
  }
  return rows;
}

/**
 * A workspace file beside the thread: highlighted code with line numbers,
 * rendered Markdown, HTML, CSV and images, editing for those who may use the
 * agent, and line comments for the composer.
 */
export function FileViewer({
  agentId,
  path,
  line,
  canUse,
  composerKey,
  onOpenFile,
}: {
  agentId: string;
  /** Path relative to the home (absolute when outside it). */
  path: string;
  /** The line to show highlighted. */
  line?: number;
  canUse: boolean;
  /** Draft key of the composer line comments go to. */
  composerKey?: string;
  /** Opens a file a rendered document links to. */
  onOpenFile?: (path: string, line?: number) => void;
}) {
  const kind = viewKind(path);
  const media = kind === 'image' || kind === 'pdf';
  const outside = path.startsWith('/');
  const url = contentUrl(agentId, outside ? path : `${AGENT_HOME}/${path}`);
  const q = useQuery({
    ...fileTextQuery(agentId, path),
    enabled: !media && !outside,
  });
  const renderable =
    kind === 'markdown' || kind === 'html' || kind === 'csv' || kind === 'tsv';
  const [view, setView] = useState<'rendered' | 'raw'>(
    line ? 'raw' : 'rendered',
  );
  const [draft, setDraft] = useState<string | null>(null);
  const editing = draft !== null;
  const f = q.data;
  const dirty = editing && draft !== f?.content;
  const canEdit = canUse && !!f && !f.isBinary && !f.truncated;
  const qc = useQueryClient();
  const save = useMutation({
    mutationFn: (content: string) =>
      unwrap(
        api.PUT('/agents/{agentId}/files/write', {
          params: { path: { agentId } },
          body: { path, content },
        }),
      ),
    onSuccess: (res, content) => {
      qc.setQueryData<TextFile>(
        fileTextQuery(agentId, path).queryKey,
        (old) => ({
          ...(old ?? res),
          ...res,
          content,
          truncated: false,
        }),
      );
    },
  });
  const onSave = () => {
    if (draft !== null && dirty && !save.isPending) save.mutate(draft);
  };

  const absPath = outside ? path : `${AGENT_HOME}/${path}`;
  const header = (
    <div className="flex shrink-0 items-center gap-1 border-b px-3 py-1.5">
      <span
        className="min-w-0 flex-1 truncate font-mono text-xs"
        title={absPath}
      >
        <span className="text-muted-foreground">
          {dirName(path) ? `${dirName(path)}/` : ''}
        </span>
        {baseName(path)}
        {line ? <span className="text-muted-foreground">:{line}</span> : null}
        {dirty && (
          <span
            role="img"
            aria-label="Unsaved changes"
            title="Unsaved changes"
            className="ml-1.5 inline-block size-2 rounded-full bg-amber-500 align-middle"
          />
        )}
      </span>
      {renderable && !editing && f && (
        <div
          role="radiogroup"
          aria-label="View"
          className="flex rounded-lg border p-0.5"
        >
          {(['rendered', 'raw'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={view === v}
              onClick={() => setView(v)}
              className={cn(
                'rounded-md px-2 py-0.5 text-xs capitalize',
                view === v
                  ? 'bg-accent font-medium text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {v}
            </button>
          ))}
        </div>
      )}
      {canEdit && !editing && (
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-muted-foreground"
          onClick={() => {
            setDraft(f.content);
            save.reset();
          }}
        >
          <Pencil className="size-3.5" aria-hidden />
          Edit
        </Button>
      )}
      {editing && (
        <>
          <Button
            size="sm"
            className="h-7 px-2"
            disabled={!dirty || save.isPending}
            title="Save (⌘S)"
            onClick={onSave}
          >
            <Save className="size-3.5" aria-hidden />
            Save
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-muted-foreground"
            onClick={() => {
              setDraft(null);
              save.reset();
            }}
          >
            <X className="size-3.5" aria-hidden />
            Done
          </Button>
        </>
      )}
      <CopyButton text={absPath} label="Copy path" />
      <a
        href={url}
        download={baseName(path)}
        aria-label="Download"
        title="Download"
        className="inline-flex items-center rounded-md px-1.5 py-1 text-muted-foreground hover:bg-accent hover:text-foreground"
      >
        <Download className="size-3.5" aria-hidden />
      </a>
    </div>
  );

  let body: ReactNode;
  if (outside) {
    body = (
      <p className="p-4 text-sm text-muted-foreground">
        This file is outside the agent's home; download it to read it.
      </p>
    );
  } else if (kind === 'image') {
    body = (
      <div className="flex justify-center p-4">
        <img
          src={url}
          alt={baseName(path)}
          className="max-h-[70vh] max-w-full rounded-lg border bg-[repeating-conic-gradient(var(--color-muted)_0_25%,transparent_0_50%)] bg-[length:16px_16px] object-contain"
        />
      </div>
    );
  } else if (kind === 'pdf') {
    body = (
      <iframe
        title={baseName(path)}
        src={url}
        className="h-full min-h-[60vh] w-full flex-1 border-0"
      />
    );
  } else if (q.isPending) {
    body = <Loading />;
  } else if (q.error) {
    body = (
      <div className="p-4">
        <ErrorText error={q.error} />
        {q.error instanceof ApiError && q.error.status === 409 && (
          <p className="mt-1 text-xs text-muted-foreground">
            Start the agent to read its files.
          </p>
        )}
      </div>
    );
  } else if (f && f.isBinary) {
    body = (
      <p className="p-4 text-sm text-muted-foreground">
        Binary file ({formatSize(f.size)}, {f.mimeType || 'unknown type'}).{' '}
        <a href={url} download={baseName(path)} className="underline">
          Download
        </a>
      </p>
    );
  } else if (f && editing) {
    body = (
      <textarea
        aria-label={`Edit ${baseName(path)}`}
        spellCheck={false}
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
            e.preventDefault();
            onSave();
          } else if (e.key === 'Tab' && !e.shiftKey) {
            // Indent instead of leaving the editor.
            e.preventDefault();
            const t = e.currentTarget;
            const { selectionStart: a, selectionEnd: b } = t;
            const v = `${t.value.slice(0, a)}\t${t.value.slice(b)}`;
            setDraft(v);
            requestAnimationFrame(() => t.setSelectionRange(a + 1, a + 1));
          }
        }}
        className="min-h-0 w-full flex-1 resize-none bg-transparent p-3 font-mono text-xs leading-5 outline-none"
      />
    );
  } else if (f && renderable && view === 'rendered') {
    body =
      kind === 'markdown' ? (
        <div className="p-4 text-sm leading-6">
          <Suspense fallback={<Loading />}>
            <Markdown
              text={f.content}
              onOpenFile={
                onOpenFile &&
                ((p, l) => onOpenFile(resolveFileRef(p, dirName(path)), l))
              }
            />
          </Suspense>
        </div>
      ) : kind === 'html' ? (
        <iframe
          title={`${baseName(path)} preview`}
          sandbox=""
          srcDoc={f.content}
          className="h-full min-h-[60vh] w-full flex-1 border-0 bg-white"
        />
      ) : (
        <DelimitedTable text={f.content} sep={kind === 'tsv' ? '\t' : ','} />
      );
  } else if (f) {
    body = (
      <CodeLines file={f} path={path} line={line} composerKey={composerKey} />
    );
  }

  return (
    <div
      data-testid="file-viewer"
      className="flex min-h-0 flex-1 flex-col"
      onKeyDown={(e) => {
        if (editing && (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's')
          e.preventDefault();
      }}
    >
      {header}
      {save.error && (
        <div className="border-b px-3 py-1.5">
          <ErrorText error={save.error} />
        </div>
      )}
      {save.isSuccess && !dirty && editing && (
        <p
          role="status"
          className="border-b px-3 py-1 text-xs text-muted-foreground"
        >
          Saved
        </p>
      )}
      {f?.truncated && (
        <p className="border-b px-3 py-1 text-xs text-muted-foreground">
          Showing the start of a {formatSize(f.size)} file.
        </p>
      )}
      <div
        className={cn(
          'min-h-0 flex-1',
          editing || kind === 'pdf' || (kind === 'html' && view === 'rendered')
            ? 'flex flex-col'
            : 'overflow-auto',
        )}
      >
        {body}
      </div>
    </div>
  );
}

function DelimitedTable({ text, sep }: { text: string; sep: string }) {
  const rows = useMemo(() => parseDelimited(text, sep), [text, sep]);
  if (rows.length === 0)
    return <p className="p-4 text-sm text-muted-foreground">Empty file</p>;
  const [head, ...rest] = rows;
  return (
    <div className="overflow-auto p-3">
      <table className="border-collapse text-xs">
        <thead>
          <tr>
            {head.map((h, i) => (
              <th
                key={i}
                className="border bg-muted/40 px-2 py-1 text-left font-medium"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rest.map((r, i) => (
            <tr key={i} className="even:bg-muted/20">
              {head.map((_, k) => (
                <td key={k} className="border px-2 py-1 whitespace-nowrap">
                  {r[k] ?? ''}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CodeLines({
  file,
  path,
  line,
  composerKey,
}: {
  file: TextFile;
  path: string;
  line?: number;
  composerKey?: string;
}) {
  const lines = useMemo(
    () => highlightLines(file.content.replace(/\n$/, ''), langOfPath(path)),
    [file.content, path],
  );
  const first = file.startLine ?? 1;
  const [sel, setSel] = useState<LineSelection | null>(null);
  const [attached, setAttached] = useState<boolean | null>(null);
  const target = useRef<HTMLTableRowElement>(null);
  useEffect(() => {
    target.current?.scrollIntoView?.({ block: 'center' });
  }, [line]);
  const bounds = sel ? selectionBounds(sel) : null;
  const text = file.content.split('\n');

  return (
    <div>
      {attached !== null && (
        <div className="border-b px-3 py-1">
          <AttachedNote rich={attached} />
        </div>
      )}
      <table className="w-full border-collapse font-mono text-xs leading-5">
        <tbody>
          {lines.map((t, i) => {
            const n = first + i;
            const sel = !!bounds && n >= bounds[0] && n <= bounds[1];
            return (
              <Fragment key={i}>
                <tr
                  ref={n === line ? target : undefined}
                  aria-current={n === line ? 'true' : undefined}
                  aria-selected={sel || undefined}
                  className={cn(
                    n === line && 'bg-amber-400/15',
                    sel && 'bg-sky-500/20',
                  )}
                >
                  <td className="w-px border-r border-border/50 align-top select-none">
                    <button
                      type="button"
                      aria-label={`Line ${n}`}
                      disabled={!composerKey}
                      onClick={(e) => {
                        setSel((s) => nextSelection(s, n, e.shiftKey));
                        setAttached(null);
                      }}
                      className="block w-full min-w-10 px-2 text-right text-muted-foreground/70 hover:text-foreground disabled:cursor-default disabled:hover:text-muted-foreground/70"
                    >
                      {n}
                    </button>
                  </td>
                  <td className="px-3 whitespace-pre">
                    <Tokens tokens={t} />
                  </td>
                </tr>
                {bounds && composerKey && bounds[1] === n && (
                  <tr>
                    <td colSpan={2} className="px-2">
                      <CommentBox
                        label={
                          bounds[0] === bounds[1]
                            ? `Line ${bounds[0]}`
                            : `Lines ${bounds[0]}-${bounds[1]}`
                        }
                        onCancel={() => setSel(null)}
                        onSubmit={(body) => {
                          setAttached(
                            attachToComposer(composerKey, {
                              kind: 'review',
                              id: newContextId(),
                              path,
                              startLine: bounds[0],
                              endLine: bounds[1],
                              source: 'file',
                              excerpt: text
                                .slice(bounds[0] - first, bounds[1] - first + 1)
                                .join('\n'),
                              body,
                            }),
                          );
                          setSel(null);
                        }}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
