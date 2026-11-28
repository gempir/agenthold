import {
  isValidElement,
  useMemo,
  type ComponentProps,
  type ReactNode,
} from 'react';
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { CopyButton } from '@/components/workspace/CopyButton';
import { Tokens } from '@/components/workspace/Tokens';
import { parseFileRef } from '@/lib/files';
import { highlightLines, langOf } from '@/lib/highlight';

/** Opens a workspace file (as linked: absolute, ~ or relative) at a line. */
export type OpenFile = (path: string, line?: number) => void;

// File links ("file:///…", "main.go:42") are kept (react-markdown drops
// unknown schemes) so they can open in the file viewer.
const urlTransform = (url: string) =>
  parseFileRef(url) ? url : defaultUrlTransform(url);

function textOf(n: ReactNode): string {
  if (typeof n === 'string' || typeof n === 'number') return String(n);
  if (Array.isArray(n)) return n.map(textOf).join('');
  if (isValidElement<{ children?: ReactNode }>(n))
    return textOf(n.props.children);
  return '';
}

/** A fenced code block: highlighted, with its language and a copy button. */
export function CodeBlock({ code, lang }: { code: string; lang?: string }) {
  const text = code.replace(/\n$/, '');
  const lines = useMemo(() => highlightLines(text, langOf(lang)), [text, lang]);
  return (
    <div
      data-testid="code-block"
      className="group/code my-3 overflow-hidden rounded-xl border bg-card"
    >
      <div className="flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-1">
        <span className="font-mono text-[11px] text-muted-foreground">
          {lang || 'text'}
        </span>
        <CopyButton text={text} label="Copy code" showLabel />
      </div>
      <pre className="m-0 overflow-x-auto p-3 text-[13px] leading-relaxed">
        <code>
          {lines.map((t, i) => (
            <span key={i} className="block min-h-[1lh]">
              <Tokens tokens={t} />
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}

export default function Markdown({
  text,
  onOpenFile,
}: {
  text: string;
  /** Makes links to workspace files open in the file viewer. */
  onOpenFile?: OpenFile;
}) {
  const components = useMemo(
    () => ({
      pre: ({ children }: ComponentProps<'pre'>) => {
        const code = isValidElement<{
          className?: string;
          children?: ReactNode;
        }>(children)
          ? children
          : null;
        const lang = code?.props.className?.match(/language-(\S+)/)?.[1];
        return (
          <CodeBlock
            code={textOf(code?.props.children ?? children)}
            lang={lang}
          />
        );
      },
      a: ({
        href,
        children,
        node,
        ...rest
      }: ComponentProps<'a'> & { node?: unknown }) => {
        void node; // the hast node is not an attribute
        const ref = onOpenFile ? parseFileRef(href) : null;
        if (!ref) {
          const external = !!href && /^https?:/i.test(href);
          return (
            <a
              href={href}
              {...rest}
              {...(external && { target: '_blank', rel: 'noreferrer' })}
            >
              {children}
            </a>
          );
        }
        // "foo.go:42" as the link text names the line when the href does not.
        const line =
          ref.line ??
          Number(textOf(children).match(/:(\d+)(?::\d+)?$/)?.[1] ?? 0);
        return (
          <a
            {...rest}
            href={href}
            data-file-link=""
            title={`Open ${ref.path}${line ? `:${line}` : ''}`}
            onClick={(e) => {
              if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0)
                return;
              e.preventDefault();
              onOpenFile?.(ref.path, line || undefined);
            }}
          >
            {children}
          </a>
        );
      },
    }),
    [onOpenFile],
  );
  return (
    <div className="max-w-none break-words [&_a]:underline [&_a]:underline-offset-2 [&_a[data-file-link]]:text-sky-700 [&_a[data-file-link]]:decoration-sky-500/40 dark:[&_a[data-file-link]]:text-sky-400 [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[0.85em] [&_h1]:mt-5 [&_h1]:mb-2 [&_h1]:text-lg [&_h1]:font-semibold [&_h2]:mt-5 [&_h2]:mb-2 [&_h2]:text-base [&_h2]:font-semibold [&_h3]:mt-4 [&_h3]:mb-1 [&_h3]:font-semibold [&_hr]:my-4 [&_li]:my-1 [&_ol]:my-2 [&_ol]:list-decimal [&_ol]:pl-6 [&_p]:my-2.5 [&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-[13px] [&_table]:my-3 [&_table]:border-collapse [&_td]:border [&_td]:px-2 [&_td]:py-1 [&_th]:border [&_th]:px-2 [&_th]:py-1 [&_th]:text-left [&_ul]:my-2 [&_ul]:list-disc [&_ul]:pl-6 [&>*:first-child]:mt-0 [&>*:last-child]:mb-0">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        urlTransform={urlTransform}
        components={components}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
