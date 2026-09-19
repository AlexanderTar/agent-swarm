import { useMemo, useState } from "react";
import { createApi, errorText } from "./api";
import { ConnectionBanner, ItemsErrorBanner, OutsideViewBanner } from "./components/Banners";
import { Header } from "./components/Header";
import { ToastProvider } from "./components/Toast";
import { C } from "./copy";
import { DataProvider, useConnection } from "./data/hooks";
import { useAgents, useItems, useRequests } from "./data/queries";
import { filterItems, isFilterActive, outsideView } from "./logic/tree";
import { Details } from "./panels/Details";
import { NeedsYou } from "./panels/NeedsYou";
import { NewItemSheet } from "./panels/NewItemSheet";
import { NewSpikeSheet } from "./panels/NewSpikeSheet";
import { Review } from "./panels/Review";
import { SpawnSheet } from "./panels/SpawnSheet";
import { NARROW, useMediaQuery } from "./state/media";
import { filterOf, useBoardUrl } from "./state/url";
import type { Item, ItemType } from "./types";
import { Dependencies } from "./views/Dependencies";
import { Hierarchy } from "./views/Hierarchy";
import { Kanban } from "./views/Kanban";
import type { SheetState, ViewProps } from "./views/props";

// F21: a fresh `[]` every render (before items load) would make the `useMemo` below never cache
// anything, since its own dependency changes identity on every call. One stable empty array fixes it.
const EMPTY_ITEMS: Item[] = [];

export function App() {
  const [url, setUrl] = useBoardUrl();
  const items = useItems();
  const agents = useAgents();
  const requests = useRequests();
  const conn = useConnection();
  const narrow = useMediaQuery(NARROW);
  const [focus, setFocus] = useState<"agents" | undefined>();
  const [sheet, setSheet] = useState<SheetState>(null);
  const filter = filterOf(url);
  const all = items.data?.items ?? EMPTY_ITEMS;
  const active = isFilterActive(filter);
  const matches = useMemo(() => (active ? filterItems(all, filter).count : null), [active, all, filter.q, filter.type, filter.status]);

  const select = (key: string, f?: "agents") => {
    setFocus(f);
    setUrl({ item: key });
  };
  const closeSheet = () => setSheet(null);
  const created = (key: string) => {
    setSheet(null);
    select(key);
  };
  const clearFilters = () => setUrl({ q: "", type: "", status: "" });
  const newItem = (type: ItemType, parentKey?: string) =>
    setSheet(type === "spike" ? { kind: "spike", caption: C.spikeViaNewItem } : { kind: "item", type, parentKey });
  const review = (req: string) => setUrl({ view: "inbox", req });
  const startOrchestrator = (item: Item) => setSheet({ kind: "spawn", itemKey: item.key });

  const viewProps: ViewProps = {
    items: all,
    loaded: items.data !== undefined,
    filter,
    selected: url.item,
    onSelect: select,
    connected: conn.connected,
  };
  const view =
    url.view === "hierarchy" ? (
      <Hierarchy {...viewProps} onAddChild={(parent, type) => newItem(type, parent)} onNewItem={() => newItem("epic")} onClearFilters={clearFilters} />
    ) : url.view === "kanban" ? (
      <Kanban
        {...viewProps}
        agents={agents.data ?? []}
        requests={requests.data ?? []}
        level={url.level}
        group={url.group}
        onLevel={(level) => setUrl({ level })}
        onReview={review}
        onClearFilters={clearFilters}
        onNewItem={() => newItem("epic")}
      />
    ) : url.view === "dependencies" ? (
      <Dependencies {...viewProps} />
    ) : (
      <NeedsYou
        filter={url.filter}
        selected={url.req}
        connected={conn.connected}
        onFilter={(f) => setUrl({ filter: f })}
        onSelectRequest={(id) => setUrl({ req: id })}
        onViewItem={(key) => setUrl({ view: "hierarchy", item: key, req: "" })}
        renderReview={(r) => <Review key={r.id} request={r} connected={conn.connected} />}
      />
    );

  const showDetails = url.item !== "" && url.view !== "inbox";
  const outside = showDetails && outsideView(url.item, all, filter, url.view, url.level);

  return (
    <div className="flex h-screen flex-col">
      {conn.state === "closed" && <ConnectionBanner onRetry={conn.retry} />}
      {items.error ? <ItemsErrorBanner message={errorText(items.error)} onRetry={items.reload} /> : null}
      <Header
        url={url}
        setUrl={setUrl}
        matches={matches}
        needsYou={requests.data?.length ?? 0}
        onNewSpike={() => setSheet({ kind: "spike" })}
        onNewItem={(t) => newItem(t, url.item || undefined)}
      />
      <main className="flex min-h-0 flex-1">
        {!(narrow && showDetails) && (
          <section data-testid="view" className="min-w-0 flex-1 overflow-auto">{view}</section>
        )}
        {showDetails && (
          <div data-testid="details" className={narrow ? "flex-1 overflow-auto" : "w-[410px] shrink-0 overflow-auto border-l border-line bg-panel"}>
            {narrow && (
              <button type="button" onClick={() => setUrl({ item: "" })} className="m-3 text-accent">{C.back}</button>
            )}
            {outside && <OutsideViewBanner onShowInHierarchy={() => setUrl({ view: "hierarchy" })} onClearFilters={clearFilters} />}
            <Details
              key={url.item}
              itemKey={url.item}
              focus={focus}
              connected={conn.connected}
              onClose={() => setUrl({ item: "" })}
              onSelect={(key) => select(key)}
              onReview={review}
              onStartOrchestrator={startOrchestrator}
            />
          </div>
        )}
      </main>
      {sheet?.kind === "spike" && <NewSpikeSheet caption={sheet.caption} onClose={closeSheet} onCreated={created} />}
      {sheet?.kind === "item" && (
        <NewItemSheet key={`${sheet.type}:${sheet.parentKey ?? ""}`} type={sheet.type} parentKey={sheet.parentKey} onClose={closeSheet} onCreated={created} />
      )}
      {sheet?.kind === "spawn" && <SpawnSheet itemKey={sheet.itemKey} onClose={closeSheet} />}
    </div>
  );
}

export default function Root() {
  const api = useMemo(() => createApi(), []);
  return (
    <DataProvider api={api}>
      <ToastProvider>
        <App />
      </ToastProvider>
    </DataProvider>
  );
}
