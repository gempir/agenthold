import { Loader2 } from 'lucide-react';

export function Loading({ label = 'Loading' }: { label?: string }) {
  return (
    <div
      role="status"
      className="flex items-center gap-2 p-6 text-sm text-muted-foreground"
    >
      <Loader2 className="size-4 animate-spin" aria-hidden />
      {label}...
    </div>
  );
}

export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : 'Something went wrong';
  return (
    <p role="alert" className="text-sm text-destructive">
      {msg}
    </p>
  );
}
