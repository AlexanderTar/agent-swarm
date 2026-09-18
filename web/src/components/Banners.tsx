import { C } from "../copy";

export const ConnectionBanner = ({ onRetry }: { onRetry(): void }) => (
  <div role="alert" className="flex items-center gap-3 bg-bad/10 px-4 py-2 text-bad">
    <span>{C.daemonDown}</span>
    <button type="button" onClick={onRetry} className="underline">{C.retry}</button>
  </div>
);

export const OutsideViewBanner = (p: { onShowInHierarchy(): void; onClearFilters(): void }) => (
  <div className="flex flex-wrap items-center gap-2 border-b border-line bg-raised px-3 py-2">
    <span>{C.outsideView}</span>
    <button type="button" onClick={p.onShowInHierarchy} className="text-accent">{C.showInHierarchy}</button>
    <button type="button" onClick={p.onClearFilters} className="text-accent">{C.clearFilters}</button>
  </div>
);
