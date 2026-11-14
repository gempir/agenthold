import { render } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { vi } from 'vitest';
import { createAppRouter } from './router';

type Handler = (req: { method: string; body: unknown }) => {
  status?: number;
  body?: unknown;
};

/** Stubs global fetch with a handler table keyed by "METHOD /path". */
export function mockApi(routes: Record<string, Handler | unknown>) {
  const calls: { key: string; body: unknown }[] = [];
  const fn = vi.fn(
    async (input: Request | string | URL, init?: RequestInit) => {
      const req =
        input instanceof Request
          ? input
          : new Request(new URL(String(input), 'http://x'), init);
      const method = req.method;
      const path = new URL(req.url).pathname.replace(/^\/api\/v1/, '');
      const key = `${method} ${path}`;
      const text = method === 'GET' ? '' : await req.text();
      const body = text ? JSON.parse(text) : undefined;
      calls.push({ key, body });
      const h = routes[key];
      if (h === undefined) {
        return new Response(JSON.stringify({ error: `unmocked ${key}` }), {
          status: 500,
        });
      }
      const r =
        typeof h === 'function'
          ? (h as Handler)({ method, body })
          : { body: h };
      const status = r.status ?? 200;
      return new Response(
        status === 204 ? null : JSON.stringify(r.body ?? {}),
        {
          status,
          headers: { 'Content-Type': 'application/json' },
        },
      );
    },
  );
  vi.stubGlobal('fetch', fn);
  return { calls };
}

export function renderApp(initialPath: string) {
  const { router, queryClient } = createAppRouter({ initialPath });
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { router };
}
