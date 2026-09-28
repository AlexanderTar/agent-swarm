import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

export function Segmented<T extends string>(p: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange(v: T): void;
  disabled?: boolean;
}) {
  return (
    <ToggleGroup
      type="single"
      variant="outline"
      size="sm"
      aria-label={p.label}
      value={p.value}
      disabled={p.disabled}
      onValueChange={(v) => { if (v) p.onChange(v as T); }}
    >
      {p.options.map((o) => (
        <ToggleGroupItem key={o.value} value={o.value} className="h-7 px-3 data-[state=on]:bg-accent data-[state=on]:text-foreground">
          {o.label}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  );
}
