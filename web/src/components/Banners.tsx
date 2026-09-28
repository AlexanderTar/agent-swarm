import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { C } from "../copy";

function AlertBanner({ message, onRetry }: { message: string; onRetry(): void }) {
  return (
    <Alert variant="destructive">
      <AlertDescription className="flex flex-wrap items-center gap-2">
        <span>{message}</span>
        <Button type="button" variant="link" size="sm" onClick={onRetry}>{C.retry}</Button>
      </AlertDescription>
    </Alert>
  );
}

export const ConnectionBanner = ({ onRetry }: { onRetry(): void }) => <AlertBanner message={C.daemonDown} onRetry={onRetry} />;

// Shell-level ruling: a failed items load is a banner, not a ViewProps field — views keep their own
// empty/placeholder state, and this is what tells the user the load itself failed and lets them retry.
export const ItemsErrorBanner = ({ message, onRetry }: { message: string; onRetry(): void }) => <AlertBanner message={message} onRetry={onRetry} />;

export const OutsideViewBanner = (p: { onShowInHierarchy(): void; onClearFilters(): void }) => (
  <Alert>
    <AlertDescription className="flex flex-wrap items-center gap-2">
      <span>{C.outsideView}</span>
      <Button type="button" variant="link" size="sm" onClick={p.onShowInHierarchy}>{C.showInHierarchy}</Button>
      <Button type="button" variant="link" size="sm" onClick={p.onClearFilters}>{C.clearFilters}</Button>
    </AlertDescription>
  </Alert>
);
