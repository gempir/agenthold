import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { FolderKanban, Plus } from 'lucide-react';
import { api, unwrap } from '@/api/client';
import { projectsQuery, type ProjectInput } from '@/api/queries';
import { ProjectForm } from '@/components/ProjectForm';
import { ErrorText, Loading } from '@/components/Spinner';
import { Button } from '@/components/ui/button';
import { Crumb, TopBar } from '@/components/layout/TopBar';
import { useDocumentTitle } from '@/hooks/useDocumentTitle';

/** The user's projects, and a form for a new one. */
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
    <>
      <TopBar>
        <Crumb current>Projects</Crumb>
      </TopBar>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-4 py-6 md:px-10 md:py-8">
          <header className="flex flex-wrap items-start gap-3">
            <div className="min-w-0 flex-1">
              <h1 className="text-xl font-semibold tracking-tight">Projects</h1>
              <p className="mt-1 text-sm text-muted-foreground">
                A project groups the repositories and folders your agents work
                on. Each thread of it works in a workspace of its own, so agents
                never get in each other&apos;s way; tasks hand work to them.
              </p>
            </div>
            {!creating && (
              <Button onClick={() => setCreating(true)}>
                <Plus className="size-4" />
                New project
              </Button>
            )}
          </header>
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
          <ul aria-label="Projects" className="grid gap-2">
            {projects.data?.map((p) => (
              <li key={p.id}>
                <Link
                  to="/projects/$projectId"
                  params={{ projectId: p.id }}
                  className="flex items-start gap-3 rounded-xl border p-3 hover:bg-accent/40"
                >
                  <FolderKanban
                    className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                    aria-hidden
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block font-medium">{p.name}</span>
                    {p.description && (
                      <span className="block truncate text-sm text-muted-foreground">
                        {p.description}
                      </span>
                    )}
                    <span className="mt-1 block truncate text-xs text-muted-foreground">
                      {p.sources.length === 0
                        ? 'No sources'
                        : p.sources.map((s) => s.name).join(' · ')}
                    </span>
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </>
  );
}
