export interface Quality {
  brightness: number;
  sharpness: number;
  tooDark: boolean;
  tooBlurry: boolean;
}

export interface PreparedPhoto {
  ai: Blob;
  width: number;
  height: number;
  quality: Quality;
}

// Long edge of the copy sent to the engine: plenty for painted numbers, small
// enough to upload fast and keep cost down. Tune with `depotd bench`.
const MAX_EDGE = 1568;

// Starting thresholds; tune them with real gate photos.
const DARK_BELOW = 45;
const BLURRY_BELOW = 60;

// preparePhoto makes the engine copy: EXIF orientation applied, downscaled,
// JPEG. The original file is uploaded untouched beside it.
export async function preparePhoto(file: Blob): Promise<PreparedPhoto> {
  const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  const width = bitmap.width;
  const height = bitmap.height;
  const scale = Math.min(1, MAX_EDGE / Math.max(width, height));
  const w = Math.max(1, Math.round(width * scale));
  const h = Math.max(1, Math.round(height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  if (!ctx) throw new Error("canvas is not available");
  ctx.drawImage(bitmap, 0, 0, w, h);
  bitmap.close();
  const quality = measure(ctx, w, h);
  const ai = await new Promise<Blob>((resolve, reject) =>
    canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("could not encode photo"))), "image/jpeg", 0.85),
  );
  return { ai, width, height, quality };
}

// measure estimates brightness (mean luma, 0-255) and sharpness (variance of
// the Laplacian) on a half-resolution grayscale copy.
function measure(ctx: CanvasRenderingContext2D, w: number, h: number): Quality {
  const step = 2;
  const sw = Math.floor(w / step);
  const sh = Math.floor(h / step);
  const data = ctx.getImageData(0, 0, w, h).data;
  const gray = new Float32Array(sw * sh);
  let sum = 0;
  for (let y = 0; y < sh; y++) {
    for (let x = 0; x < sw; x++) {
      const i = (y * step * w + x * step) * 4;
      const g = 0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2];
      gray[y * sw + x] = g;
      sum += g;
    }
  }
  const brightness = gray.length ? sum / gray.length : 0;
  let lapSum = 0;
  let lapSq = 0;
  let n = 0;
  for (let y = 1; y < sh - 1; y++) {
    for (let x = 1; x < sw - 1; x++) {
      const c = y * sw + x;
      const lap = gray[c - sw] + gray[c + sw] + gray[c - 1] + gray[c + 1] - 4 * gray[c];
      lapSum += lap;
      lapSq += lap * lap;
      n++;
    }
  }
  const mean = n ? lapSum / n : 0;
  const sharpness = n ? lapSq / n - mean * mean : 0;
  return { brightness, sharpness, tooDark: brightness < DARK_BELOW, tooBlurry: sharpness < BLURRY_BELOW };
}
