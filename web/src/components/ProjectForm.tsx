import { useState } from 'react';
import { FolderGit2, Folder, Plus, Trash2 } from 'lucide-react';
import type { Project, ProjectInput } from '@/api/queries';
import { ErrorText } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select } from '@/components/ui/select';

type Kind = 'repo' | 'folder';

interface SourceDraft {
  key: number;
  name: string;
  kind: Kind;
  url: string;
  path: string;
  defaultBranch: string;
}

let nextKey = 1;
const blank = (kind: Kind = 'repo'): SourceDraft => ({
  key: nextKey++,
  name: '',
  kind,
  url: '',
  path: '',
  defaultBranch: '',
});

export function draftOf(p?: Project) {
  return {
    name: p?.name ?? '',
    description: p?.description ?? '',
    instructions: p?.instructions ?? '',
    sources: (p?.sources ?? []).map((s): SourceDraft => ({
      key: nextKey++,
      name: s.name,
      kind: s.kind,
      url: s.url ?? '',
      path: s.path ?? '',
      defaultBranch: s.defaultBranch ?? '',
    })),
  };
}

function toInput(d: ReturnType<typeof draftOf>): ProjectInput {
  return {
    name: d.name.trim(),
    description: d.description,
    instructions: d.instructions,
    sources: d.sources
      .filter((s) => s.name.trim() || s.url.trim() || s.path.trim())
      .map((s) => ({
        kind: s.kind,
        ...(s.name.trim() && { name: s.name.trim() }),
        ...(s.kind === 'repo' && s.url.trim() && { url: s.url.trim() }),
        ...(s.path.trim() && { path: s.path.trim() }),
        ...(s.defaultBranch.trim() && {
          defaultBranch: s.defaultBranch.trim(),
        }),
      })),
  };
}

/** The editable fields of a project: name, notes for agents and sources. */
export function ProjectForm({
  project,
  submitLabel,
  pending,
  error,
  onSubmit,
  onCancel,
}: {
  project?: Project;
  submitLabel: string;
  pending?: boolean;
  error?: unknown;
  onSubmit: (input: ProjectInput) => void;
  onCancel?: () => void;
}) {
  const [d, setD] = useState(() => {
    const init = draftOf(project);
    if (!project) init.sources = [blank()];
    return init;
  });
  const setSource = (key: number, patch: Partial<SourceDraft>) =>
    setD({
      ...d,
      sources: d.sources.map((s) => (s.key === key ? { ...s, ...patch } : s)),
    });

  return (
    <form
      className="flex flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(toInput(d));
      }}
    >
      <label className="flex flex-col gap-1.5 text-sm">
        <span className="font-medium">Name</span>
        <Input
          value={d.name}
          onChange={(e) => setD({ ...d, name: e.target.value })}
          placeholder="Web shop"
          maxLength={100}
          required
        />
      </label>
      <label className="flex flex-col gap-1.5 text-sm">
        <span className="font-medium">Description</span>
        <Input
          value={d.description}
          onChange={(e) => setD({ ...d, description: e.target.value })}
          placeholder="What the project is"
          maxLength={2000}
        />
      </label>

      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1.5 text-sm font-medium">
          Repositories and folders
        </legend>
        <p className="-mt-1 mb-1 text-xs text-muted-foreground">
          Every thread of the project gets its own git worktree of each, on a
          branch of its own. Folders become local git repositories.
        </p>
        {d.sources.map((s) => (
          <div
            key={s.key}
            className="flex flex-wrap items-center gap-2 rounded-lg border p-2"
          >
            {s.kind === 'repo' ? (
              <FolderGit2
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
            ) : (
              <Folder
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
            )}
            <Select
              aria-label="Kind"
              value={s.kind}
              onChange={(e) =>
                setSource(s.key, { kind: e.target.value as Kind })
              }
              className="w-24"
            >
              <option value="repo">Repo</option>
              <option value="folder">Folder</option>
            </Select>
            {s.kind === 'repo' && (
              <Input
                aria-label="Git URL"
                value={s.url}
                onChange={(e) => setSource(s.key, { url: e.target.value })}
                placeholder="https://github.com/owner/repo.git"
                className="min-w-48 flex-[2]"
              />
            )}
            <Input
              aria-label="Source name"
              value={s.name}
              onChange={(e) => setSource(s.key, { name: e.target.value })}
              placeholder={s.kind === 'repo' ? 'name (from URL)' : 'name'}
              className="w-32 flex-1"
            />
            <Input
              aria-label="Existing path"
              value={s.path}
              onChange={(e) => setSource(s.key, { path: e.target.value })}
              placeholder="path in home (optional)"
              className="w-40 flex-1"
            />
            <Input
              aria-label="Default branch"
              value={s.defaultBranch}
              onChange={(e) =>
                setSource(s.key, { defaultBranch: e.target.value })
              }
              placeholder="branch (auto)"
              className="w-28"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="Remove source"
              onClick={() =>
                setD({
                  ...d,
                  sources: d.sources.filter((x) => x.key !== s.key),
                })
              }
            >
              <Trash2 className="size-4" />
            </Button>
          </div>
        ))}
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setD({ ...d, sources: [...d.sources, blank()] })}
          >
            <Plus className="size-4" />
            Repository
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setD({ ...d, sources: [...d.sources, blank('folder')] })
            }
          >
            <Plus className="size-4" />
            Folder
          </Button>
        </div>
      </fieldset>

      <label className="flex flex-col gap-1.5 text-sm">
        <span className="font-medium">Instructions for agents</span>
        <textarea
          value={d.instructions}
          onChange={(e) => setD({ ...d, instructions: e.target.value })}
          rows={5}
          maxLength={20000}
          placeholder="How to build and test, conventions, what not to touch. Written into every workspace's AGENTS.md."
          className="rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        />
      </label>

      <ErrorText error={error} />
      <div className="flex gap-2">
        <Button type="submit" disabled={pending || !d.name.trim()}>
          {submitLabel}
        </Button>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        )}
      </div>
    </form>
  );
}
