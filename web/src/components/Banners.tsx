import { C } from "../copy";

function AlertBanner({ message, onRetry }: { message: string; onRetry(): void }) {
  return (
    <div role="alert" className="flex items-center gap-3 bg-bad/10 px-4 py-2 text-bad">
      <span>{message}</span>
      <button type="button" onClick={onRetry} className="underline">{C.retry}</button>
    </div>
  );
}

export const ConnectionBanner = ({ onRetry }: { onRetry(): void }) => <AlertBanner message={C.daemonDown} onRetry={onRetry} />;

// Shell-level ruling: a failed items load is a banner, not a ViewProps field — views keep their own
// empty/placeholder state, and this is what tells the user the load itself failed and lets them retry.
export const ItemsErrorBanner = ({ message, onRetry }: { message: string; onRetry(): void }) => <AlertBanner message={message} onRetry={onRetry} />;

export const OutsideViewBanner = (p: { onShowInHierarchy(): void; onClearFilters(): void }) => (
  <div className="flex flex-wrap items-center gap-2 border-b border-line bg-raised px-3 py-2">
    <span>{C.outsideView}</span>
    <button type="button" onClick={p.onShowInHierarchy} className="text-accent">{C.showInHierarchy}</button>
    <button type="button" onClick={p.onClearFilters} className="text-accent">{C.clearFilters}</button>
  </div>
);
