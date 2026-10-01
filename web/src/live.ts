import { useEffect, useRef } from "react";

// useMoveEvents calls onMove with a move ID whenever the hub reports a change.
// EventSource reconnects by itself after network drops.
export function useMoveEvents(onMove: (id: string) => void) {
  const handler = useRef(onMove);
  useEffect(() => {
    handler.current = onMove;
  });
  useEffect(() => {
    const source = new EventSource("/api/stream");
    const listener = (e: MessageEvent<string>) => {
      try {
        const data = JSON.parse(e.data) as { id?: string };
        if (data.id) handler.current(data.id);
      } catch {
        // ignore malformed events
      }
    };
    source.addEventListener("move", listener);
    return () => {
      source.removeEventListener("move", listener);
      source.close();
    };
  }, []);
}
