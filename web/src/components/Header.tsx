import type { ReactNode } from "react";
import { ChevronDown, ListFilter, Search } from "lucide-react";
import { C, STATUS_LABEL, T, TYPE_LABEL, TYPE_PLURAL } from "../copy";
import { effectiveGrouping } from "../logic/kanban";
import { useMediaQuery } from "../state/media";
import type { BoardUrl } from "../state/url";
import { ITEM_STATUSES } from "../types";
import type { CardLevel, Grouping, ItemStatus, ItemType, View } from "../types";
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
function TypeStatus({ url, setUrl, fieldClass }: Pick<HeaderProps, "url" | "setUrl"> & { fieldClass: string }) {
  return <>
    <Field label={C.type} className={fieldClass}>
      <Select value={url.type || ALL} onValueChange={(v) => setUrl({ type: (v === ALL ? "" : v) as ItemType | "" })}>
        <SelectTrigger aria-label={C.type}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value={ALL}>{C.all}</SelectItem>{TYPES.map((t) => <SelectItem key={t} value={t}>{TYPE_PLURAL[t]}</SelectItem>)}</SelectContent>
      </Select>
    </Field>
    <Field label={C.status} className={fieldClass}>
      <Select value={url.status || ALL} onValueChange={(v) => setUrl({ status: (v === ALL ? "" : v) as ItemStatus | "" })}>
        <SelectTrigger aria-label={C.status}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value={ALL}>{C.all}</SelectItem>{ITEM_STATUSES.map((s) => <SelectItem key={s} value={s}>{STATUS_LABEL[s]}</SelectItem>)}</SelectContent>
      </Select>
    </Field>
  </>;
}

function LevelGroup({ url, setUrl, fieldClass }: Pick<HeaderProps, "url" | "setUrl"> & { fieldClass: string }) {
  return <>
    <Field label={C.cardLevel} className={fieldClass}>
      <Select value={url.level} onValueChange={(v) => setUrl({ level: v as CardLevel })}>
        <SelectTrigger aria-label={C.cardLevel}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value="tasks">{C.tasks}</SelectItem><SelectItem value="stories">{C.stories}</SelectItem><SelectItem value="top">{C.topLevel}</SelectItem></SelectContent>
      </Select>
    </Field>
    <Field label={C.groupBy} className={fieldClass}>
      <Select value={effectiveGrouping(url.level, url.group)} disabled={url.level === "top"} onValueChange={(v) => setUrl({ group: v as Grouping })}>
        <SelectTrigger aria-label={C.groupBy}><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value="root">{C.groupRoot}</SelectItem><SelectItem value="flat">{C.flat}</SelectItem></SelectContent>
      </Select>
    </Field>
  </>;
}

function FiltersPopover(p: HeaderProps) {
  const active = (p.url.type ? 1 : 0) + (p.url.status ? 1 : 0);
  const stacked = "flex flex-col gap-1.5";
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button type="button" variant="outline" aria-label={active > 0 ? T.filtersActive(active) : C.filters}>
          <ListFilter className="size-4" />{C.filters}
          {active > 0 && <span className="rounded-full bg-primary px-1.5 text-xs text-primary-foreground">{active}</span>}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" aria-label={C.filters} className="flex w-[min(20rem,calc(100vw-2rem))] flex-col gap-3">
        <TypeStatus url={p.url} setUrl={p.setUrl} fieldClass={stacked} />
        {p.url.view === "kanban" && <LevelGroup url={p.url} setUrl={p.setUrl} fieldClass={stacked} />}
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

export function Header(p: HeaderProps) {
  const { url, setUrl } = p;
  const narrow = useMediaQuery(BELOW_SM);
  const inline = "flex items-center gap-1.5";
  return (
    <header className="space-y-2 border-b border-border bg-card px-4 py-2 pt-[max(0.5rem,env(safe-area-inset-top))] pl-[max(1rem,env(safe-area-inset-left))] pr-[max(1rem,env(safe-area-inset-right))]">
      <div className="flex min-h-12 flex-wrap items-center gap-2 py-2 sm:h-12 sm:flex-nowrap sm:py-0">
        <h1 className="text-[14px] font-semibold">{C.appTitle}</h1>
        <Button type="button" variant="secondary" className="ml-auto" onClick={() => setUrl({ view: "inbox" })}>
          {p.needsYou > 0 && <span data-dot className="size-1.5 rounded-full bg-warning" />}
          {T.needsYouButton(p.needsYou)}
        </Button>
        <span aria-hidden className="basis-full sm:hidden" />
        <Button type="button" variant="outline" onClick={p.onNewSpike}>{C.newOrchestrator}</Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button">{C.newItem}<ChevronDown className="size-4" /></Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" aria-label={C.newItem}>
            {NEW_TYPES.map((t) => <DropdownMenuItem key={t} onSelect={() => p.onNewItem(t)}>{TYPE_LABEL[t]}</DropdownMenuItem>)}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className={narrow ? "relative min-w-0 flex-1" : "relative"}>
          <Search className="pointer-events-none absolute left-2 top-2 size-4 text-muted-foreground max-md:top-3.5" />
          <Input type="search" aria-label={C.search} placeholder={C.search} value={url.q} onChange={(e) => setUrl({ q: e.target.value })} className={narrow ? "w-full pl-8" : "h-8 w-64 pl-8"} />
        </div>
        {narrow ? <FiltersPopover {...p} /> : <>
          <TypeStatus url={url} setUrl={setUrl} fieldClass={inline} />
          {p.matches !== null && <>
            <Button type="button" variant="link" onClick={() => setUrl({ q: "", type: "", status: "" })}>{C.clearFilters}</Button>
            <span className="ml-auto text-muted-foreground">{T.matches(p.matches)}</span>
          </>}
        </>}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Tabs value={url.view} onValueChange={(v) => setUrl({ view: v as View })}>
          <TabsList aria-label={C.view}>{VIEWS.map((v) => <TabsTrigger key={v.value} value={v.value}>{v.label}</TabsTrigger>)}</TabsList>
        </Tabs>
        {!narrow && url.view === "kanban" && <LevelGroup url={url} setUrl={setUrl} fieldClass={inline} />}
      </div>
    </header>
  );
}
