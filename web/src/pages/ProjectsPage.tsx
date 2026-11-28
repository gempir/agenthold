import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { FolderKanban, Plus } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { projectsQuery, type ProjectInput } from '@/api/queries';
import { ProjectForm } from '@/components/ProjectForm';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';

/** Account settings: the user's projects, and a form for a new one. */
export function ProjectsPage({ create }: { create?: boolean }) {
  useDocumentTitle('Projects');
  const qc = useQueryClient();
  const navigate = useNavigate();
  const projects = useQuery(projectsQuery);
  const [creating, setCreating] = useState(!!create);
  const add = useMutation({
    mutationFn: (body: ProjectInput) => unwrap(api.POST('/projects', { body })),
    onSuccess: async (p) => {
      await qc.invalidateQueries({ queryKey: ['projects'] });
      void navigate({
        to: '/projects/$projectId',
        params: { projectId: p.id },
      });
    },
  });

  return (
    <div className="flex flex-col gap-6">
      <SettingsHeader
        title="Projects"
        description="A project groups the repositories and folders your agents work on. Each thread works in its own workspace so agents stay out of each other's way."
      >
        {!creating && (
          <Button onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            New project
          </Button>
        )}
      </SettingsHeader>
      {creating && (
        <section className="rounded-xl border p-4">
          <h2 className="mb-4 text-base font-medium">New project</h2>
          <ProjectForm
            submitLabel="Create project"
            pending={add.isPending}
            error={add.error}
            onSubmit={(b) => add.mutate(b)}
            onCancel={() => setCreating(false)}
          />
        </section>
      )}
      {projects.isLoading && <Loading />}
      <ErrorText error={projects.error} />
      {projects.data?.length === 0 && !creating && (
        <p className="text-sm text-muted-foreground">No projects yet.</p>
      )}
      {!!projects.data?.length && (
        <ul
          aria-label="Projects"
          className="flex flex-col divide-y overflow-hidden rounded-xl border"
        >
          {projects.data.map((p) => (
            <li key={p.id}>
              <Link
                to="/projects/$projectId"
                params={{ projectId: p.id }}
                className="flex items-start gap-3 px-4 py-3 text-sm hover:bg-accent/60"
              >
                <FolderKanban
                  className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                  aria-hidden
                />
                <span className="min-w-0 flex-1">
                  <span className="block font-medium">{p.name}</span>
                  {p.description && (
                    <span className="block truncate text-muted-foreground">
                      {p.description}
                    </span>
                  )}
                  <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                    {p.sources.length === 0
                      ? 'No sources'
                      : p.sources.map((s) => s.name).join(' · ')}
                  </span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
