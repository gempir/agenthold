import { useState, type ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { ArrowUpRight, Check, X } from 'lucide-react';
import type { PermissionMode, Session } from '@/api/queries';
import { useDraft } from '@/lib/drafts';
import { ErrorText } from '@/components/Spinner';
import {
  Composer,
  DEFAULT_COMPOSER_PLACEHOLDER,
} from '@/components/chat/Composer';
import { parseSlashCommand } from '@/components/chat/ComposerMenu';
import {
  toImagePayload,
  uploadFileAttachments,
  useAttachments,
  type FileAttachment,
} from '@/lib/attachments';
import {
  serializeComposerContext,
  useComposerContext,
  type ComposerContextChip,
} from '@/lib/composerContext';

export interface DraftSendOptions {
  background?: boolean;
}

/**
 * A conversation that has not started yet: a greeting and the composer,
 * centred like a blank page. Sending creates the session (or parallel sessions
 * when multiple models are selected; Cmd/Ctrl+Enter starts in background).
 */
export function DraftChat({
  draftKey,
  agentId,
  cwd,
  heading,
  sub,
  meta,
  notice,
  placeholder = DEFAULT_COMPOSER_PLACEHOLDER,
  readOnly,
  disabled,
  pending,
  error,
  backgroundSessions,
  onDismissBackground,
  onPermissionMode,
  onSend,
  children,
}: {
  /** Where the unsent text is kept, so it survives leaving the page. */
  draftKey: string;
  agentId?: string;
  cwd?: string;
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
  /** Recently background-started threads (Cmd/Ctrl+Enter). */
  backgroundSessions?: Session[];
  onDismissBackground?: () => void;
  onPermissionMode?: (mode: PermissionMode) => void;
  onSend: (
    text: string,
    images: ReturnType<typeof toImagePayload>,
    opts?: DraftSendOptions,
  ) => void;
  children?: ReactNode;
}) {
  const [text, setText] = useDraft(draftKey);
  const att = useAttachments();
  const ctx = useComposerContext(draftKey);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<Error | null>(null);

  const openModelPicker = () => {
    document
      .querySelector<HTMLButtonElement>('[data-model-picker-trigger="true"]')
      ?.click();
  };

  const handleSlashCommand = (command: string) => {
    switch (command) {
      case 'clear':
        setText('');
        att.clear();
        ctx.clear();
        setUploadError(null);
        return;
      case 'model':
        openModelPicker();
        return;
      case 'plan':
        onPermissionMode?.('plan');
        return;
    }
  };

  const submit = async (background: boolean) => {
    setUploadError(null);
    const trimmed = text.trim();
    const slash = parseSlashCommand(trimmed);
    if (slash) {
      if (slash.command === 'clear') {
        handleSlashCommand('clear');
        return;
      }
      if (slash.command === 'model') {
        setText('');
        openModelPicker();
        return;
      }
      if (slash.command === 'plan') {
        setText('');
        onPermissionMode?.('plan');
        return;
      }
    }

    let promptBody = trimmed;
    if (slash?.command === 'compact') {
      promptBody = slash.note
        ? `Please summarize and compact the conversation context. Focus on: ${slash.note}`
        : 'Please summarize and compact the conversation context.';
    } else if (slash?.command === 'goal' && slash.criteria) {
      promptBody = `Goal: ${slash.criteria}\n\nWork thoroughly and verify all completion criteria before finishing.`;
    }

    let uploadedChips: ComposerContextChip[] = [];
    if (att.files.length > 0 && agentId) {
      setUploading(true);
      try {
        const uploaded = await uploadFileAttachments(agentId, att.files);
        uploadedChips = uploaded.map((u) => ({
          id: `uploaded:${u.id}`,
          kind: 'file',
          path: u.path,
          label: u.name,
          size: u.size,
        }));
      } catch (e) {
        setUploadError(
          e instanceof Error ? e : new Error('Could not upload attachment'),
        );
        return;
      } finally {
        setUploading(false);
      }
    }

    const finalText = serializeComposerContext(
      [...ctx.chips, ...uploadedChips],
      promptBody,
    );
    const images = toImagePayload(att.items);
    if (!finalText && images.length === 0) return;

    onSend(finalText, images, { background });
    if (background) {
      setText('');
      att.clear();
      ctx.clear();
    } else {
      ctx.clear();
    }
  };

  const handleInlineLargePaste = (f: FileAttachment) => {
    if (!f.pasteText) return;
    setText(text ? `${text}\n${f.pasteText}` : f.pasteText);
    att.removeFile(f.id);
  };

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
        {backgroundSessions && backgroundSessions.length > 0 && (
          <div
            role="status"
            data-testid="background-started"
            className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-xl border bg-card/80 px-3 py-2 text-xs"
          >
            <div className="flex flex-wrap items-center gap-2">
              <Check
                className="size-3.5 shrink-0 text-emerald-500"
                aria-hidden
              />
              <span className="text-foreground">
                Started{' '}
                {backgroundSessions.length === 1
                  ? 'thread'
                  : `${backgroundSessions.length} threads`}{' '}
                in background
              </span>
              {backgroundSessions.map((s) => (
                <Link
                  key={s.id}
                  to="/a/$agentId"
                  params={{ agentId: s.agentId }}
                  search={{ s: s.id }}
                  className="inline-flex items-center gap-1 rounded-md border bg-background px-2 py-0.5 font-medium text-foreground hover:bg-accent"
                >
                  <span className="max-w-40 truncate">
                    {s.title || 'Open thread'}
                  </span>
                  <ArrowUpRight className="size-3" aria-hidden />
                </Link>
              ))}
            </div>
            {onDismissBackground && (
              <button
                type="button"
                aria-label="Dismiss"
                onClick={onDismissBackground}
                className="rounded p-0.5 text-muted-foreground hover:text-foreground"
              >
                <X className="size-3.5" />
              </button>
            )}
          </div>
        )}
        {readOnly ? (
          <p className="rounded-2xl border border-dashed px-4 py-3 text-center text-sm text-muted-foreground">
            You have read-only access to this agent.
          </p>
        ) : (
          <>
            <ErrorText error={uploadError ?? error} />
            <Composer
              value={text}
              onChange={setText}
              busy={pending || uploading}
              disabled={disabled}
              autoFocus
              meta={meta}
              agentId={agentId}
              cwd={cwd}
              placeholder={placeholder}
              attachments={att.items}
              fileAttachments={att.files}
              contextChips={ctx.chips}
              attachError={att.error}
              onAttach={att.add}
              onRemoveAttachment={att.remove}
              onRemoveFileAttachment={att.removeFile}
              onLargePaste={(pasted) => void att.addLargePaste(pasted)}
              onInlineLargePaste={handleInlineLargePaste}
              onAddContextChip={ctx.add}
              onRemoveContextChip={ctx.remove}
              onSlashCommand={handleSlashCommand}
              onSubmit={() => void submit(false)}
              onBackgroundSubmit={() => void submit(true)}
            />
          </>
        )}
      </div>
    </div>
  );
}
