import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, File, Folder, Search, Trash2 } from 'lucide-react';
import { api, ApiError, unwrap } from '@/api/client';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';
import {
  AGENT_HOME,
  breadcrumbs,
  contentUrl,
  formatSize,
  homeRel,
  joinPath,
} from '@/lib/files';

type Entry = {
  name: string;
  size: number;
  isDir: boolean;
  modTime: string;
};

export function FilesTab({
  agentId,
  canUse,
  onStart,
  starting,
  onOpenFile,
}: {
  agentId: string;
  canUse: boolean;
  onStart: () => void;
  starting: boolean;
  /** Opens a file (path relative to the home) in the file viewer; without
   * it, a click downloads the file. */
  onOpenFile?: (path: string) => void;
}) {
  const qc = useQueryClient();
  const [path, setPath] = useState('');
  const [mkdirOpen, setMkdirOpen] = useState(false);
  const [folder, setFolder] = useState('');
  const [toDelete, setToDelete] = useState<Entry | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [filter, setFilter] = useState('');
  const term = useDebounced(filter.trim(), 200);
  const key = ['agents', agentId, 'files', path];

  const q = useQuery({
    queryKey: key,
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/files', {
          params: { path: { agentId }, query: { path } },
        }),
      ),
    retry: false,
  });
  const dir = q.data?.path ?? path;
  // Search works relative to the home; outside it there is no filter.
  const base = homeRel(dir);
  const searchable = !base.startsWith('/');
  const search = useQuery({
    queryKey: ['agents', agentId, 'files', 'search', base, term],
    queryFn: () =>
      unwrap(
        api.GET('/agents/{agentId}/files/search', {
          params: {
            path: { agentId },
            query: { ...(base && { path: base }), q: term, limit: 50 },
          },
        }),
      ),
    enabled: !!term && searchable && !!q.data,
    retry: false,
  });
  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['agents', agentId, 'files'] });

  const mkdir = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/agents/{agentId}/files/mkdir', {
          params: { path: { agentId } },
          body: { path: joinPath(dir, folder.trim()) },
        }),
      ),
    onSuccess: async () => {
      setMkdirOpen(false);
      setFolder('');
      await refresh();
    },
  });

  const del = useMutation({
    mutationFn: (e: Entry) =>
      unwrap(
        api.DELETE('/agents/{agentId}/files', {
          params: {
            path: { agentId },
            query: { path: joinPath(dir, e.name), recursive: e.isDir },
          },
        }),
      ),
    onSuccess: async () => {
      setToDelete(null);
      await refresh();
    },
  });

  const upload = useMutation({
    mutationFn: async (file: File) => {
      const res = await fetch(contentUrl(agentId, joinPath(dir, file.name)), {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/octet-stream' },
        body: file,
      });
      if (!res.ok) {
        let msg = `Upload failed (${res.status})`;
        try {
          const j = (await res.json()) as { error?: string };
          if (j.error) msg = j.error;
        } catch {
          // ignore
        }
        throw new ApiError(res.status, msg);
      }
    },
    onSuccess: refresh,
  });

  if (q.isPending) return <Loading />;
  if (q.error) {
    if (q.error instanceof ApiError && q.error.status === 409) {
      return (
        <div className="grid justify-items-start gap-3">
          <p role="alert" className="text-sm">
            {q.error.message}
          </p>
          {canUse && (
            <Button
              disabled={starting}
              onClick={() => {
                onStart();
              }}
            >
              Start
            </Button>
          )}
        </div>
      );
    }
    return <ErrorText error={q.error} />;
  }

  const entries = [...q.data.entries].sort(
    (a, b) => Number(b.isDir) - Number(a.isDir) || a.name.localeCompare(b.name),
  );

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <nav aria-label="Path" className="flex flex-wrap items-center text-sm">
          <button
            type="button"
            className="rounded px-1 hover:bg-accent"
            onClick={() => setPath('')}
          >
            ~
          </button>
          {breadcrumbs(q.data.path).map((b) => (
            <span key={b.path} className="flex items-center">
              <span className="text-muted-foreground">/</span>
              {b.path === '/' ? null : (
                <button
                  type="button"
                  className="rounded px-1 hover:bg-accent"
                  onClick={() => setPath(b.path)}
                >
                  {b.name}
                </button>
              )}
            </span>
          ))}
        </nav>
        {canUse && (
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setMkdirOpen(true)}
            >
              New folder
            </Button>
            <Button
              size="sm"
              disabled={upload.isPending}
              onClick={() => fileInput.current?.click()}
            >
              Upload
            </Button>
            <input
              ref={fileInput}
              type="file"
              hidden
              aria-label="Upload file"
              onChange={(e) => {
                const f = e.target.files?.[0];
                e.target.value = '';
                if (f) upload.mutate(f);
              }}
            />
          </div>
        )}
      </div>
      <ErrorText error={upload.error} />
      <ErrorText error={del.error} />

      {searchable && (
        <label className="relative block">
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
            aria-hidden
          />
          <Input
            aria-label="Find files"
            placeholder="Find files by name"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={(e) => e.key === 'Escape' && setFilter('')}
            className="h-8 pl-8 text-sm"
          />
        </label>
      )}

      {term && searchable ? (
        <SearchResults
          loading={search.isPending}
          error={search.error}
          results={search.data?.results ?? []}
          truncated={!!search.data?.truncated}
          onPick={(m) => {
            const rel = base ? `${base}/${m.path}` : m.path;
            setFilter('');
            if (m.isDir) setPath(`${AGENT_HOME}/${rel}`);
            else if (onOpenFile) onOpenFile(rel);
            else
              window.location.assign(
                contentUrl(agentId, `${AGENT_HOME}/${rel}`),
              );
          }}
        />
      ) : (
        <ul className="divide-y rounded-lg border text-sm">
          {entries.length === 0 && (
            <li className="p-3 text-muted-foreground">Empty directory</li>
          )}
          {entries.map((e) => {
            const abs = joinPath(q.data.path, e.name);
            return (
              <li key={e.name} className="flex items-center gap-3 px-3 py-2">
                {e.isDir ? (
                  <Folder className="size-4 shrink-0" aria-hidden />
                ) : (
                  <File className="size-4 shrink-0" aria-hidden />
                )}
                {e.isDir ? (
                  <button
                    type="button"
                    className="min-w-0 flex-1 truncate text-left hover:underline"
                    onClick={() => setPath(abs)}
                  >
                    {e.name}
                  </button>
                ) : onOpenFile ? (
                  <button
                    type="button"
                    className="min-w-0 flex-1 truncate text-left hover:underline"
                    onClick={() => onOpenFile(homeRel(abs))}
                  >
                    {e.name}
                  </button>
                ) : (
                  <a
                    className="min-w-0 flex-1 truncate hover:underline"
                    href={contentUrl(agentId, abs)}
                    download={e.name}
                  >
                    {e.name}
                  </a>
                )}
                <span className="w-20 text-right text-muted-foreground">
                  {e.isDir ? '' : formatSize(e.size)}
                </span>
                <span
                  className={cn(
                    'hidden w-44 text-muted-foreground',
                    !onOpenFile && 'sm:inline',
                  )}
                >
                  {new Date(e.modTime).toLocaleString()}
                </span>
                {onOpenFile && !e.isDir && (
                  <a
                    href={contentUrl(agentId, abs)}
                    download={e.name}
                    aria-label={`Download ${e.name}`}
                    title="Download"
                    className="inline-flex size-9 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
                  >
                    <Download className="size-4" aria-hidden />
                  </a>
                )}
                {canUse && (
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label={`Delete ${e.name}`}
                    onClick={() => setToDelete(e)}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <Dialog open={mkdirOpen} onOpenChange={setMkdirOpen}>
        <DialogContent>
          <form
            onSubmit={(ev) => {
              ev.preventDefault();
              if (folder.trim()) mkdir.mutate();
            }}
            className="grid gap-4"
          >
            <DialogHeader>
              <DialogTitle>New folder</DialogTitle>
            </DialogHeader>
            <Input
              autoFocus
              aria-label="Folder name"
              value={folder}
              onChange={(e) => setFolder(e.target.value)}
            />
            <ErrorText error={mkdir.error} />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setMkdirOpen(false)}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={!folder.trim() || mkdir.isPending}
              >
                Create
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {toDelete?.name}?</DialogTitle>
          </DialogHeader>
          {toDelete?.isDir && (
            <p className="text-sm text-muted-foreground">
              The folder and everything in it will be deleted.
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setToDelete(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={del.isPending}
              onClick={() => toDelete && del.mutate(toDelete)}
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

type Match = { path: string; name: string; isDir: boolean };

function SearchResults({
  loading,
  error,
  results,
  truncated,
  onPick,
}: {
  loading: boolean;
  error: unknown;
  results: Match[];
  truncated: boolean;
  onPick: (m: Match) => void;
}) {
  if (loading) return <Loading />;
  if (error) return <ErrorText error={error} />;
  return (
    <ul
      aria-label="Matching files"
      className="divide-y rounded-lg border text-sm"
    >
      {results.length === 0 && (
        <li className="p-3 text-muted-foreground">No matching files</li>
      )}
      {results.map((m) => (
        <li key={m.path}>
          <button
            type="button"
            onClick={() => onPick(m)}
            className="flex w-full items-center gap-3 px-3 py-2 text-left hover:bg-accent/60"
          >
            {m.isDir ? (
              <Folder className="size-4 shrink-0" aria-hidden />
            ) : (
              <File className="size-4 shrink-0" aria-hidden />
            )}
            <span className="min-w-0 flex-1 truncate">
              {m.name}
              <span className="ml-2 text-xs text-muted-foreground">
                {m.path}
              </span>
            </span>
          </button>
        </li>
      ))}
      {truncated && (
        <li className="p-2 text-xs text-muted-foreground">
          More files match; refine the name.
        </li>
      )}
    </ul>
  );
}
