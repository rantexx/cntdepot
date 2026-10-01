import { useState, type FormEvent } from "react";
import { api, ApiError } from "../api";
import { useT } from "../i18n";

export function Login({ onDone }: { onDone: () => void }) {
  const { t } = useT();
  const [code, setCode] = useState("");
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    try {
      await api.login(code);
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setError(t("wrongCode"));
      else if (err instanceof ApiError && err.status === 429) setError(t("tooManyTries"));
      else setError(t("networkError"));
    }
  }

  return (
    <form className="card narrow" onSubmit={submit}>
      <h2>{t("accessCode")}</h2>
      <input type="password" value={code} onChange={(e) => setCode(e.target.value)} autoFocus />
      <button type="submit" className="primary">
        {t("login")}
      </button>
      {error && <p className="error">{error}</p>}
    </form>
  );
}
