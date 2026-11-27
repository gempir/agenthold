import { useState, type ReactNode } from 'react';
import { ErrorText } from '@/components/Spinner';
import { Composer } from '@/components/chat/Composer';
import { toImagePayload, useAttachments } from '@/lib/attachments';

/**
 * A conversation that has not started yet: a greeting and the composer,
 * centred like a blank page. Sending creates the session.
 */
export function DraftChat({
  heading,
  sub,
  meta,
  notice,
  placeholder = 'Message the agent',
  readOnly,
  disabled,
  pending,
  error,
  onSend,
  children,
}: {
  heading: ReactNode;
  sub?: ReactNode;
  meta?: ReactNode;
  notice?: ReactNode;
  placeholder?: string;
  /** Viewers cannot start conversations. */
  readOnly?: boolean;
  disabled?: boolean;
  pending?: boolean;
  error?: unknown;
  onSend: (text: string, images: ReturnType<typeof toImagePayload>) => void;
  children?: ReactNode;
}) {
  const [text, setText] = useState('');
  const att = useAttachments();
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col justify-center px-3 py-10 md:px-8">
        <div className="mb-8 px-1 text-center">
          <h1 className="text-2xl font-medium tracking-tight md:text-3xl">
            {heading}
          </h1>
          {sub && <p className="mt-2 text-sm text-muted-foreground">{sub}</p>}
        </div>
        {children}
        {notice}
        {readOnly ? (
          <p className="rounded-2xl border border-dashed px-4 py-3 text-center text-sm text-muted-foreground">
            You have read-only access to this agent.
          </p>
        ) : (
          <>
            <ErrorText error={error} />
            <Composer
              value={text}
              onChange={setText}
              busy={pending}
              disabled={disabled}
              autoFocus
              meta={meta}
              placeholder={placeholder}
              attachments={att.items}
              attachError={att.error}
              onAttach={att.add}
              onRemoveAttachment={att.remove}
              onSubmit={() => onSend(text.trim(), toImagePayload(att.items))}
            />
          </>
        )}
      </div>
    </div>
  );
}
