import createClient from 'openapi-fetch';
import type { paths } from './schema';

export const api = createClient<paths>({
  // Same-origin API path; the origin prefix keeps Request construction valid
  // in non-browser runtimes (tests).
  baseUrl: `${globalThis.location?.origin ?? ''}/api/v1`,
  credentials: 'same-origin',
  // Resolve fetch lazily so tests can stub it.
  fetch: (req) => globalThis.fetch(req),
});

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** Unwraps an openapi-fetch result, throwing ApiError with the `{error}` message. */
export async function unwrap<T>(p: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) {
    const msg =
      error && typeof error === 'object' && 'error' in error
        ? String((error as { error: unknown }).error)
        : `Request failed (${response.status})`;
    throw new ApiError(response.status, msg);
  }
  return data as T;
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : 'Something went wrong';
}
