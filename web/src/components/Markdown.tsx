import ReactMarkdown from "react-markdown";

export const Markdown = ({ children }: { children: string }) => (
  <div className="min-w-0 space-y-2 [overflow-wrap:anywhere] text-foreground [&_a]:text-link [&_code]:rounded [&_code]:bg-muted [&_code]:font-mono [&_h2]:text-base [&_h2]:font-semibold [&_li]:ml-5 [&_li]:list-disc [&_pre]:whitespace-pre-wrap [&_pre]:rounded [&_pre]:bg-muted [&_pre]:p-2">
    <ReactMarkdown>{children}</ReactMarkdown>
  </div>
);
