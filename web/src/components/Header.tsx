import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { ChevronDown, ListFilter, Search } from "lucide-react";
import { C, STATUS_LABEL, T, TYPE_LABEL, TYPE_PLURAL } from "../copy";
import { effectiveGrouping } from "../logic/kanban";
import { useMediaQuery } from "../state/media";
import type { BoardUrl } from "../state/url";
import { ITEM_STATUSES } from "../types";
import type { CardLevel, Grouping, ItemStatus, ItemType, View } from "../types";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";
import { Input } from "./ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

const BELOW_SM = "(max-width: 639px)";

const ALL = "__all";
const TYPES: ItemType[] = ["epic", "story", "task", "bug", "spike", "chore"];
const NEW_TYPES: ItemType[] = ["epic", "bug", "story", "task", "spike", "chore"];
const VIEWS: { value: View; label: string }[] = [
  { value: "hierarchy", label: C.hierarchy },
  { value: "kanban", label: C.kanban },
  { value: "dependencies", label: C.dependencies },
];

type HeaderProps = {
  url: BoardUrl;
  setUrl(patch: Partial<BoardUrl>): void;
  matches: number | null;
  needsYou: number;
  onNewSpike(): void;
  onNewItem(type: ItemType): void;
};

const Field = ({ label, className, children }: { label: string; className: string; children: ReactNode }) => (
  <label className={className}>{label}{children}</label>
);

// The four filter/option selects, laid out inline at sm and up and stacked in the Filters popover below sm.
function TypeStatus({ url, setUrl, fieldClass, triggerClass }: Pick<HeaderProps, "url" | "setUrl"> & { fieldClass: string; triggerClass?: string }) {
  return <>
    <Field label={C.type} className={fieldClass}>
      <Select value={url.type || ALL} onValueChange={(v) => setUrl({ type: (v === ALL ? "" : v) as ItemType | "" })}>
        <SelectTrigger aria-label={C.type} className={triggerClass}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value={ALL}>{C.all}</SelectItem>{TYPES.map((t) => <SelectItem key={t} value={t}>{TYPE_PLURAL[t]}</SelectItem>)}</SelectContent>
      </Select>
    </Field>
    <Field label={C.status} className={fieldClass}>
      <Select value={url.status || ALL} onValueChange={(v) => setUrl({ status: (v === ALL ? "" : v) as ItemStatus | "" })}>
        <SelectTrigger aria-label={C.status} className={triggerClass}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value={ALL}>{C.all}</SelectItem>{ITEM_STATUSES.map((s) => <SelectItem key={s} value={s}>{STATUS_LABEL[s]}</SelectItem>)}</SelectContent>
      </Select>
    </Field>
  </>;
}

function LevelGroup({ url, setUrl, fieldClass, triggerClass }: Pick<HeaderProps, "url" | "setUrl"> & { fieldClass: string; triggerClass?: string }) {
  return <>
    <Field label={C.cardLevel} className={fieldClass}>
      <Select value={url.level} onValueChange={(v) => setUrl({ level: v as CardLevel })}>
        <SelectTrigger aria-label={C.cardLevel} className={triggerClass}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value="tasks">{C.tasks}</SelectItem><SelectItem value="stories">{C.stories}</SelectItem><SelectItem value="top">{C.topLevel}</SelectItem></SelectContent>
      </Select>
    </Field>
    <Field label={C.groupBy} className={fieldClass}>
      <Select value={effectiveGrouping(url.level, url.group)} disabled={url.level === "top"} onValueChange={(v) => setUrl({ group: v as Grouping })}>
        <SelectTrigger aria-label={C.groupBy} className={triggerClass}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value="root">{C.groupRoot}</SelectItem><SelectItem value="flat">{C.flat}</SelectItem></SelectContent>
      </Select>
    </Field>
  </>;
}

function FiltersPopover(p: HeaderProps) {
  const active = (p.url.type ? 1 : 0) + (p.url.status ? 1 : 0);
  const stacked = "flex flex-col gap-1.5";
  const full = "w-full";
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button type="button" variant="outline" size="icon" className="relative shrink-0" aria-label={active > 0 ? T.filtersActive(active) : C.filters}>
          <ListFilter className="size-4" />
          {active > 0 && <Badge className="absolute -right-1.5 -top-1.5 h-4 min-w-4 px-1">{active}</Badge>}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" collisionPadding={16} aria-label={C.filters} className="flex w-[min(20rem,calc(100vw-2rem))] flex-col gap-3">
        <TypeStatus url={p.url} setUrl={p.setUrl} fieldClass={stacked} triggerClass={full} />
        {p.url.view === "kanban" && <LevelGroup url={p.url} setUrl={p.setUrl} fieldClass={stacked} triggerClass={full} />}
        {p.matches !== null && (
          <div className="flex items-center justify-between">
            <Button type="button" variant="link" className="px-0" onClick={() => p.setUrl({ q: "", type: "", status: "" })}>{C.clearFilters}</Button>
            <span className="text-muted-foreground">{T.matches(p.matches)}</span>
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

const SAFE_AREA = "pt-[max(0.5rem,env(safe-area-inset-top))] pl-[max(1rem,env(safe-area-inset-left))] pr-[max(1rem,env(safe-area-inset-right))]";

// `compact` shows only the dot and count; the accessible name keeps the full label.
function NeedsYouButton({ count, onClick, className, compact }: { count: number; onClick(): void; className?: string; compact?: boolean }) {
  return (
    <Button type="button" variant="secondary" className={cn("shrink-0", className)} aria-label={compact ? T.needsYouButton(count) : undefined} onClick={onClick}>
      {count > 0 && <span data-dot className="size-1.5 rounded-full bg-warning" />}
      {compact ? count : T.needsYouButton(count)}
    </Button>
  );
}

function NewItemMenu(p: Pick<HeaderProps, "onNewSpike" | "onNewItem"> & { orchestratorItem?: boolean }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button">{C.newItem}<ChevronDown className="size-4" /></Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" aria-label={C.newItem}>
        {p.orchestratorItem && <DropdownMenuItem onSelect={p.onNewSpike}>{C.newOrchestrator}</DropdownMenuItem>}
        {NEW_TYPES.map((t) => <DropdownMenuItem key={t} onSelect={() => p.onNewItem(t)}>{TYPE_LABEL[t]}</DropdownMenuItem>)}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ViewTabs({ url, setUrl, className }: Pick<HeaderProps, "url" | "setUrl"> & { className?: string }) {
  return (
    <Tabs value={url.view} onValueChange={(v) => setUrl({ view: v as View })} className={className}>
      <TabsList aria-label={C.view} className={className && "w-full"}>{VIEWS.map((v) => <TabsTrigger key={v.value} value={v.value} className={className && "px-1"}>{v.label}</TabsTrigger>)}</TabsList>
    </Tabs>
  );
}

// Below sm everything fits two 44 px rows: views + Needs you, then search + Filters + New. The app title
// stays for screen readers only, and New orchestrator moves into the New menu.
function NarrowHeader(p: HeaderProps) {
  const { url, setUrl } = p;
  return (
    <header className={cn("space-y-2 border-b border-border bg-card px-4 py-2", SAFE_AREA)}>
      <h1 className="sr-only">{C.appTitle}</h1>
      <div className="flex items-center gap-2">
        <ViewTabs url={url} setUrl={setUrl} className="min-w-0 flex-1" />
        <NeedsYouButton compact count={p.needsYou} onClick={() => setUrl({ view: "inbox" })} />
      </div>
      <div className="flex items-center gap-2">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-2 top-3.5 size-4 text-muted-foreground" />
          <Input type="search" aria-label={C.search} placeholder={C.search} value={url.q} onChange={(e) => setUrl({ q: e.target.value })} className="w-full pl-8" />
        </div>
        <FiltersPopover {...p} />
        <NewItemMenu onNewSpike={p.onNewSpike} onNewItem={p.onNewItem} orchestratorItem />
      </div>
    </header>
  );
}

export function Header(p: HeaderProps) {
  const { url, setUrl } = p;
  const narrow = useMediaQuery(BELOW_SM);
  if (narrow) return <NarrowHeader {...p} />;
  const inline = "flex items-center gap-1.5";
  return (
    <header className={cn("space-y-2 border-b border-border bg-card px-4 py-2", SAFE_AREA)}>
      <div className="flex min-h-12 flex-wrap items-center gap-2 py-2 sm:h-12 sm:flex-nowrap sm:py-0">
        <h1 className="text-[14px] font-semibold">{C.appTitle}</h1>
        <NeedsYouButton count={p.needsYou} onClick={() => setUrl({ view: "inbox" })} className="ml-auto" />
        <Button type="button" variant="outline" onClick={p.onNewSpike}>{C.newOrchestrator}</Button>
        <NewItemMenu onNewSpike={p.onNewSpike} onNewItem={p.onNewItem} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-2 size-4 text-muted-foreground max-md:top-3.5" />
          <Input type="search" aria-label={C.search} placeholder={C.search} value={url.q} onChange={(e) => setUrl({ q: e.target.value })} className="h-8 w-64 pl-8" />
        </div>
        <TypeStatus url={url} setUrl={setUrl} fieldClass={inline} />
        {p.matches !== null && <>
          <Button type="button" variant="link" onClick={() => setUrl({ q: "", type: "", status: "" })}>{C.clearFilters}</Button>
          <span className="ml-auto text-muted-foreground">{T.matches(p.matches)}</span>
        </>}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <ViewTabs url={url} setUrl={setUrl} />
        {url.view === "kanban" && <LevelGroup url={url} setUrl={setUrl} fieldClass={inline} />}
      </div>
    </header>
  );
}
