import { useEffect, useRef } from "react";

// useMoveEvents calls onMove with a move ID whenever the hub reports a change.
// EventSource reconnects by itself after network drops; onOpen fires on every
// (re)connect, so a page can reload what it missed while the link was down.
export function useMoveEvents(onMove: (id: string) => void, onOpen?: () => void) {
  const handler = useRef(onMove);
  const openHandler = useRef(onOpen);
  useEffect(() => {
    handler.current = onMove;
    openHandler.current = onOpen;
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
    const opened = () => openHandler.current?.();
    source.addEventListener("move", listener);
    source.addEventListener("open", opened);
    return () => {
      source.removeEventListener("move", listener);
      source.removeEventListener("open", opened);
      source.close();
    };
  }, []);
}
