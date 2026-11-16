import { useState } from "react";
import { testRead, type TestReadResult } from "../gen/api";
import { ok, timeout, UPLOAD_TIMEOUT_MS } from "../hubApi";
import { CandidateList, CheckDigitMath, PhotoWithBox } from "../components";
import { useT } from "../i18n";
import { preparePhoto, type Quality } from "../image";

export function TestRead() {
  const { t } = useT();
  const [preview, setPreview] = useState("");
  const [quality, setQuality] = useState<Quality | null>(null);
  const [result, setResult] = useState<TestReadResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function onFile(files: FileList | null) {
    const file = files?.[0];
    if (!file) return;
    setBusy(true);
    setError("");
    setResult(null);
    try {
      const prepared = await preparePhoto(file);
      if (preview) URL.revokeObjectURL(preview);
      setPreview(URL.createObjectURL(prepared.ai));
      setQuality(prepared.quality);
      setResult(await ok(testRead({ ai: prepared.ai }, timeout(UPLOAD_TIMEOUT_MS))));
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  const top = result?.frame.candidates?.[0]?.number ?? "";
  return (
    <div className="page">
      <div className="page-head">
        <h1>{t("navTest")}</h1>
      </div>
      <label className="shot-button">
        {t("choosePhoto")}
        <input
          type="file"
          accept="image/*"
          capture="environment"
          hidden
          onChange={(e) => {
            void onFile(e.target.files);
            e.target.value = "";
          }}
        />
      </label>
      {busy && <p>{t("status_reading")}</p>}
      {error && <p className="error">{error}</p>}
      {quality && (
        <p className="small">
          {t("quality")}: {quality.brightness.toFixed(0)} / {quality.sharpness.toFixed(0)}
          {quality.tooDark && ` · ${t("tooDark")}`}
          {quality.tooBlurry && ` · ${t("tooBlurry")}`}
        </p>
      )}
      {preview && <PhotoWithBox src={preview} box={result?.attempt.reading.box} alt="" />}
      {result && (
        <div className="card">
          {result.error && (
            <p className="error">
              {t("error")}: {result.error}
            </p>
          )}
          <CandidateList candidates={result.frame.candidates} />
          {top && <CheckDigitMath number={top} />}
          <p>
            {t("typeCode")}: <b>{result.frame.type_code || "–"}</b> · {t("color")}: <b>{result.frame.color || "–"}</b>
          </p>
          <p className="small">
            {t("engine")}: {result.attempt.engine}/{result.attempt.model} · {t("latency")}: {result.attempt.latency_ms} ms ·{" "}
            {t("tokens")}: {result.attempt.usage.input_tokens} / {result.attempt.usage.output_tokens}
          </p>
          <details>
            <summary>{t("rawAnswer")}</summary>
            <pre>{result.attempt.raw}</pre>
          </details>
        </div>
      )}
    </div>
  );
}
