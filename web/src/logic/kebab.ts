// Port of Go ids.Kebab (§4). Keep it in step with internal/ids.
// The daemon owns collision suffixes (-2, -3, …); this only normalises.
export function kebab(input: string, max = 48): string {
  const ascii = input.normalize("NFKD").replace(/[^\x00-\x7F]/g, "");
  let s = ascii.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  if (s.length > max) {
    const cut = s.slice(0, max);
    const at = s[max] === "-" ? max : cut.lastIndexOf("-");
    s = (at > 0 ? cut.slice(0, at) : cut).replace(/-+$/, "");
  }
  return s;
}

export function defaultOrchestratorName(title: string): string {
  const base = kebab(title, 24);
  return base ? `${base}-orchestrator` : "orchestrator";
}
