import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { api, unwrap } from '@/api/client';
import { ACCOUNT_SETTINGS_NAV } from '@/pages/AccountSettings';
import { projectsQuery } from '@/api/queries';
import { useChatIndex } from '@/hooks/useChatIndex';
import { useShell } from './shell';
import { getTheme, setTheme } from '@/lib/theme';

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [debounced, setDebounced] = useState('');
  const [selected, setSelected] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const previousFocus = useRef<HTMLElement | null>(null);
  const navigate = useNavigate();
  const shell = useShell();
  const index = useChatIndex();
  const projects = useQuery(projectsQuery);
  useEffect(() => {
    const show = () => {
      previousFocus.current = document.activeElement as HTMLElement;
      setOpen(true);
      setSelected(0);
    };
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        show();
      }
    };
    window.addEventListener('keydown', key);
    window.addEventListener('agenthold:search', show);
    return () => {
      window.removeEventListener('keydown', key);
      window.removeEventListener('agenthold:search', show);
    };
  }, []);
  useEffect(() => {
    if (open) input.current?.focus();
  }, [open]);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(query.trim()), 250);
    return () => clearTimeout(t);
  }, [query]);
  const search = useQuery({
    queryKey: ['session-search', debounced],
    enabled: open && debounced.length > 0,
    queryFn: () =>
      unwrap(
        api.GET('/sessions/search', { params: { query: { q: debounced } } }),
      ),
  });
  const close = () => {
    setOpen(false);
    setQuery('');
    previousFocus.current?.focus();
  };
  const items: {
    id: string;
    label: string;
    snippet?: string;
    run: () => void;
  }[] = [];
  const match = (s: string) => s.toLowerCase().includes(query.toLowerCase());
  for (const { session, agent } of index.chats)
    if (
      match(
        [
          session.title,
          session.git?.branch,
          session.git?.project,
          session.project?.name,
          session.project?.branch,
        ].join(' '),
      )
    )
      items.push({
        id: session.id,
        label: session.title || 'New thread',
        run: () => {
          void navigate({
            to: '/a/$agentId',
            params: { agentId: agent.id },
            search: { s: session.id },
          });
        },
      });
  for (const result of search.data ?? []) {
    const found = items.find((i) => i.id === result.session.id);
    if (found) found.snippet = result.snippet;
    else
      items.push({
        id: result.session.id,
        label: result.session.title,
        snippet: result.snippet,
        run: () => {
          void navigate({
            to: '/a/$agentId',
            params: { agentId: result.session.agentId },
            search: { s: result.session.id },
          });
        },
      });
  }
  for (const p of projects.data ?? [])
    if (match(p.name)) {
      items.push({
        id: 'project-' + p.id,
        label: 'Project: ' + p.name,
        run: () => {
          void navigate({
            to: '/projects/$projectId',
            params: { projectId: p.id },
          });
        },
      });
      items.push({
        id: 'new-' + p.id,
        label: 'New thread in ' + p.name,
        run: () => {
          void navigate({
            to: '/projects/$projectId',
            params: { projectId: p.id },
          });
        },
      });
    }
  for (const { agent } of index.agents)
    if (match(agent.name))
      items.push({
        id: 'agent-' + agent.id,
        label: 'Environment: ' + agent.name,
        run: () => {
          void navigate({
            to: '/a/$agentId/settings',
            params: { agentId: agent.id },
          });
        },
      });
  for (const [label, run] of [
    [
      'New thread',
      () => {
        void navigate({ to: '/new' });
      },
    ],
    ['Toggle sidebar', () => shell.setCollapsed(!shell.collapsed)],
    ['Toggle theme', () => setTheme(getTheme() === 'dark' ? 'light' : 'dark')],
    [
      'Toggle Group by project',
      () => window.dispatchEvent(new Event('agenthold:group-projects')),
    ],
    [
      'Open Settings',
      () => {
        void navigate({ to: '/settings' });
      },
    ],
  ] as [string, () => void][])
    if (match(label)) items.push({ id: label, label, run });
  for (const page of ACCOUNT_SETTINGS_NAV.filter((p) => !p.admin))
    if (match('Settings ' + page.label))
      items.push({
        id: page.to,
        label: 'Settings: ' + page.label,
        run: () => {
          void navigate({ to: page.to });
        },
      });
  if (!open) return null;
  const current = Math.min(selected, Math.max(0, items.length - 1));
  return (
    <div
      className="fixed inset-0 z-50 flex justify-center bg-black/50 pt-[15vh]"
      onClick={close}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="h-fit w-full max-w-xl rounded-xl border bg-background p-3 shadow-xl"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.preventDefault();
            close();
          }
          if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            setSelected(
              (current + (e.key === 'ArrowDown' ? 1 : -1) + items.length) %
                Math.max(1, items.length),
            );
          }
          if (e.key === 'Enter' && items[current]) {
            e.preventDefault();
            items[current].run();
            close();
          }
          if (e.key === 'Tab') {
            e.preventDefault();
            input.current?.focus();
          }
        }}
      >
        <input
          ref={input}
          role="combobox"
          aria-label="Search commands and threads"
          aria-expanded
          aria-controls="command-results"
          aria-activedescendant={
            items[current] ? 'command-' + current : undefined
          }
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setSelected(0);
          }}
          className="w-full rounded border p-3"
          placeholder="Search threads, projects, environments and actions…"
        />
        <div
          id="command-results"
          role="listbox"
          className="max-h-[55vh] overflow-auto"
        >
          {items.map((item, i) => (
            <button
              role="option"
              aria-selected={i === current}
              id={'command-' + i}
              key={item.id}
              className={
                'block w-full rounded p-2 text-left ' +
                (i === current ? 'bg-accent' : '')
              }
              onClick={() => {
                item.run();
                close();
              }}
            >
              <span>{item.label}</span>
              {item.snippet && (
                <p className="truncate text-xs text-muted-foreground">
                  {item.snippet}
                </p>
              )}
            </button>
          ))}
          {items.length === 0 && <p className="p-3">No results.</p>}
        </div>
      </div>
    </div>
  );
}
