import { ChevronDown, Search } from "lucide-react";
import { C, STATUS_LABEL, T, TYPE_LABEL, TYPE_PLURAL } from "../copy";
import { effectiveGrouping } from "../logic/kanban";
import type { BoardUrl } from "../state/url";
import { ITEM_STATUSES } from "../types";
import type { CardLevel, Grouping, ItemStatus, ItemType, View } from "../types";
import { Button } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

const ALL = "__all";
const TYPES: ItemType[] = ["epic", "story", "task", "bug", "spike", "chore"];
const NEW_TYPES: ItemType[] = ["epic", "bug", "story", "task", "spike", "chore"];
const VIEWS: { value: View; label: string }[] = [
  { value: "hierarchy", label: C.hierarchy },
  { value: "kanban", label: C.kanban },
  { value: "dependencies", label: C.dependencies },
];

export function Header(p: {
  url: BoardUrl;
  setUrl(patch: Partial<BoardUrl>): void;
  matches: number | null;
  needsYou: number;
  onNewSpike(): void;
  onNewItem(type: ItemType): void;
}) {
  const { url, setUrl } = p;
  return (
    <header className="space-y-2 border-b border-border bg-card px-4 py-2">
      <div className="flex h-12 items-center gap-2">
        <h1 className="text-[14px] font-semibold">{C.appTitle}</h1>
        <Button type="button" variant="secondary" className="ml-auto" onClick={() => setUrl({ view: "inbox" })}>
          {p.needsYou > 0 && <span data-dot className="size-1.5 rounded-full bg-warning" />}
          {T.needsYouButton(p.needsYou)}
        </Button>
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
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-2 size-4 text-muted-foreground" />
          <Input type="search" aria-label={C.search} placeholder={C.search} value={url.q} onChange={(e) => setUrl({ q: e.target.value })} className="h-8 w-64 pl-8" />
        </div>
        <label className="flex items-center gap-1.5">{C.type}
          <Select value={url.type || ALL} onValueChange={(v) => setUrl({ type: (v === ALL ? "" : v) as ItemType | "" })}>
            <SelectTrigger aria-label={C.type}><SelectValue /></SelectTrigger>
            <SelectContent><SelectItem value={ALL}>{C.all}</SelectItem>{TYPES.map((t) => <SelectItem key={t} value={t}>{TYPE_PLURAL[t]}</SelectItem>)}</SelectContent>
          </Select>
        </label>
        <label className="flex items-center gap-1.5">{C.status}
          <Select value={url.status || ALL} onValueChange={(v) => setUrl({ status: (v === ALL ? "" : v) as ItemStatus | "" })}>
            <SelectTrigger aria-label={C.status}><SelectValue /></SelectTrigger>
            <SelectContent><SelectItem value={ALL}>{C.all}</SelectItem>{ITEM_STATUSES.map((s) => <SelectItem key={s} value={s}>{STATUS_LABEL[s]}</SelectItem>)}</SelectContent>
          </Select>
        </label>
        {p.matches !== null && <>
          <Button type="button" variant="link" onClick={() => setUrl({ q: "", type: "", status: "" })}>{C.clearFilters}</Button>
          <span className="ml-auto text-muted-foreground">{T.matches(p.matches)}</span>
        </>}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Tabs value={url.view} onValueChange={(v) => setUrl({ view: v as View })}>
          <TabsList aria-label={C.view}>{VIEWS.map((v) => <TabsTrigger key={v.value} value={v.value}>{v.label}</TabsTrigger>)}</TabsList>
        </Tabs>
        {url.view === "kanban" && <>
          <label className="flex items-center gap-1.5">{C.cardLevel}
            <Select value={url.level} onValueChange={(v) => setUrl({ level: v as CardLevel })}>
              <SelectTrigger aria-label={C.cardLevel}><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="tasks">{C.tasks}</SelectItem><SelectItem value="stories">{C.stories}</SelectItem><SelectItem value="top">{C.topLevel}</SelectItem></SelectContent>
            </Select>
          </label>
          <label className="flex items-center gap-1.5">{C.groupBy}
            <Select value={effectiveGrouping(url.level, url.group)} disabled={url.level === "top"} onValueChange={(v) => setUrl({ group: v as Grouping })}>
              <SelectTrigger aria-label={C.groupBy}><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="root">{C.groupRoot}</SelectItem><SelectItem value="flat">{C.flat}</SelectItem></SelectContent>
            </Select>
          </label>
        </>}
      </div>
    </header>
  );
}
