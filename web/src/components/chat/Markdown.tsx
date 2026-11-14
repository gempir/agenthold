import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

export default function Markdown({ text }: { text: string }) {
  return (
    <div className="prose-sm max-w-none break-words [&_a]:underline [&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:my-2 [&_pre]:overflow-x-auto [&_pre]:rounded [&_pre]:bg-muted [&_pre]:p-2 [&_table]:border [&_td]:border [&_td]:px-2 [&_th]:border [&_th]:px-2 [&_ul]:list-disc [&_ul]:pl-5">
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  );
}
