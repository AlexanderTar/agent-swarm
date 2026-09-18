import ReactMarkdown from "react-markdown";

export const Markdown = ({ children }: { children: string }) => (
  <div className="space-y-2 [&_h2]:text-base [&_h2]:font-semibold [&_li]:ml-5 [&_li]:list-disc [&_pre]:overflow-x-auto [&_pre]:rounded [&_pre]:bg-raised [&_pre]:p-2">
    <ReactMarkdown>{children}</ReactMarkdown>
  </div>
);
