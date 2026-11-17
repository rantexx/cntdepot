import { useEffect, useRef, type PointerEvent } from "react";
import { useT } from "../i18n";

const W = 600;
const H = 200;

// Signature is the trucker's signature pad. It hands back a PNG (white ground, ink strokes)
// after every stroke, or null when cleared.
export function Signature({ onChange }: { onChange: (png: Blob | null) => void }) {
  const { t } = useT();
  const canvas = useRef<HTMLCanvasElement>(null);
  const last = useRef<{ x: number; y: number } | null>(null);

  const clear = () => {
    const ctx = canvas.current?.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, W, H);
    onChange(null);
  };
  useEffect(clear, []); // once, on mount: a white ground, so the JPEG copy is not black

  const point = (e: PointerEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    return { x: ((e.clientX - r.left) * W) / r.width, y: ((e.clientY - r.top) * H) / r.height };
  };
  const down = (e: PointerEvent<HTMLCanvasElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    last.current = point(e);
  };
  const move = (e: PointerEvent<HTMLCanvasElement>) => {
    const ctx = e.currentTarget.getContext("2d");
    if (!last.current || !ctx) return;
    const p = point(e);
    ctx.strokeStyle = "#1e2226";
    ctx.lineWidth = 3;
    ctx.lineCap = "round";
    ctx.beginPath();
    ctx.moveTo(last.current.x, last.current.y);
    ctx.lineTo(p.x, p.y);
    ctx.stroke();
    last.current = p;
  };
  const up = () => {
    if (!last.current) return;
    last.current = null;
    canvas.current?.toBlob((png) => onChange(png), "image/png");
  };

  return (
    <div className="signature">
      <canvas
        ref={canvas}
        width={W}
        height={H}
        role="img"
        aria-label={t("gateSignature")}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
      />
      <button type="button" onClick={clear}>
        {t("gateClearSignature")}
      </button>
    </div>
  );
}
