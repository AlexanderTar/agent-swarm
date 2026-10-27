import { useEffect, useState } from "react";

// The current time, refreshed every minute, for text like "busy for 12 min" that the daemon
// does not push again as the minutes pass.
export function useNow(): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(id);
  }, []);
  return now;
}
