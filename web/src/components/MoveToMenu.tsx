import { ChevronDown, Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { C } from "../copy";
import { type MoveCheck, type Movable, moveOptions } from "../logic/transitions";
import type { ItemStatus } from "../types";

export function MoveToMenu(p: {
  item: Movable;
  onMove(status: ItemStatus, check: MoveCheck): void;
  disabled?: boolean;
  buttonLabel?: string;
  ariaLabel?: string;
}) {
  const enabled = (c: MoveCheck) => c.ok || c.special !== undefined;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" disabled={p.disabled} aria-label={p.ariaLabel}>
          {p.buttonLabel ?? C.moveTo}
          <ChevronDown className="size-3.5 opacity-60" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        {moveOptions(p.item).map((o) => (
          <DropdownMenuItem key={o.status} disabled={!enabled(o.check)} onSelect={() => p.onMove(o.status, o.check)} className="flex-col items-start gap-0.5">
            <span className="flex items-center gap-1.5">
              {!enabled(o.check) && <Lock aria-hidden className="size-3" />}
              {o.label}
            </span>
            {!o.check.ok && <span className="text-xs text-muted-foreground">{o.check.reason}</span>}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
