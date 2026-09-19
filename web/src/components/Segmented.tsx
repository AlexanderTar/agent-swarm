export function Segmented<T extends string>(p: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange(v: T): void;
  disabled?: boolean;
}) {
  return (
    <div role="radiogroup" aria-label={p.label} className="inline-flex rounded-md border border-line p-0.5">
      {p.options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === p.value}
          disabled={p.disabled}
          onClick={() => p.onChange(o.value)}
          className={`rounded px-2 py-0.5 ${o.value === p.value ? "bg-raised font-medium" : "text-muted"}`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
