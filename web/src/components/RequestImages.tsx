import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { C, T } from "../copy";
import { MAX_IMAGES } from "../logic/requestImages";
import { useMediaQuery } from "../state/media";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

export interface RequestImage {
  id: string;
  name: string;
  blob: Blob;
  url: string;
}

const MD = "(min-width: 768px)";

export function RequestImages(p: {
  images: RequestImage[];
  pending: number;
  error?: string;
  disabled?: boolean;
  onAdd(files: File[]): void;
  onRemove(id: string): void;
}) {
  const desktop = useMediaQuery(MD);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const focusAfter = useRef<string | null>(null);
  const shown = useRef<RequestImage[]>([]);
  const n = p.images.length;
  const pendingKeys = Array.from({ length: p.pending }, (_, k) => `pending-${k}`);

  // Object URLs: revoked as soon as an image leaves the list, and the rest on unmount.
  useEffect(() => {
    for (const old of shown.current)
      if (!p.images.some((i) => i.id === old.id)) URL.revokeObjectURL(old.url);
    shown.current = p.images;
  }, [p.images]);
  useEffect(
    () => () => {
      for (const i of shown.current) URL.revokeObjectURL(i.url);
    },
    [],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs after the parent removes an image
  useEffect(() => {
    const target = focusAfter.current;
    if (target === null) return;
    focusAfter.current = null;
    const sel = target === "add" ? "[data-add-images]" : `[data-remove-id="${CSS.escape(target)}"]`;
    root.current?.querySelector<HTMLElement>(sel)?.focus();
  }, [p.images]);

  const remove = (id: string) => {
    const i = p.images.findIndex((x) => x.id === id);
    focusAfter.current = (p.images[i + 1] ?? p.images[i - 1])?.id ?? "add";
    p.onRemove(id);
  };

  const tile =
    "relative size-12 shrink-0 overflow-hidden rounded-md border border-border bg-muted max-md:size-auto max-md:aspect-square";
  const list = n + p.pending > 0 && (
    <ul
      className={
        desktop
          ? "flex min-w-0 gap-2 overflow-x-auto"
          : "grid grid-cols-[repeat(5,minmax(0,72px))] gap-2"
      }
    >
      {p.images.map((i) => (
        <li key={i.id} className={tile} title={i.name}>
          <img src={i.url} alt={i.name} className="size-full object-cover" draggable={false} />
          <Button
            type="button"
            variant="ghost"
            size={null}
            aria-label={T.removeImage(i.name)}
            data-remove-id={i.id}
            onClick={() => remove(i.id)}
            className="group absolute right-0 top-0 flex size-6 items-start justify-end rounded-none p-0.5 hover:bg-transparent focus-visible:ring-0 max-md:size-[44px] max-md:p-[4px]"
          >
            <span className="grid size-[18px] place-items-center rounded-full bg-black/70 text-white group-hover:bg-black/85 group-focus-visible:ring-2 group-focus-visible:ring-ring max-md:size-[24px]">
              <X className="size-3 max-md:size-[14px]" strokeWidth={2.5} />
            </span>
          </Button>
        </li>
      ))}
      {pendingKeys.map((k) => (
        <li
          key={k}
          aria-hidden="true"
          className="size-12 shrink-0 rounded-md bg-muted motion-safe:animate-pulse max-md:size-auto max-md:aspect-square"
        />
      ))}
    </ul>
  );
  const add = (
    <Button
      type="button"
      variant="outline"
      size="sm"
      data-add-images
      disabled={p.disabled || n >= MAX_IMAGES}
      onClick={() => input.current?.click()}
    >
      {C.addImages}
    </Button>
  );
  const count = n > 0 && (
    <span className="shrink-0 whitespace-nowrap text-xs text-muted-foreground tabular-nums">
      {T.imageCount(n)}
    </span>
  );

  return (
    <div ref={root}>
      <Input
        ref={input}
        type="file"
        accept="image/*"
        multiple
        hidden
        className="hidden"
        aria-hidden="true"
        tabIndex={-1}
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = "";
          if (files.length > 0) p.onAdd(files);
        }}
      />
      {desktop ? (
        <div className="flex items-center gap-2 md:gap-1.5">
          {list}
          {add}
          {count}
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            {add}
            {count}
          </div>
          {list}
        </div>
      )}
      <p aria-live="polite" className="mt-2 text-xs text-destructive empty:mt-0">
        {p.error ?? ""}
      </p>
    </div>
  );
}
