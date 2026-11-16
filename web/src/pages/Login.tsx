import { useT } from "../i18n";
import { useSessionActions } from "../session";

// The server decides how sign-in works: in development /auth/login sends the
// browser to its email form, in production to the identity provider.
export function Login() {
  const { t } = useT();
  return (
    <section className="card narrow">
      <h2>{t("appTitle")}</h2>
      <p className="muted">{t("signInHint")}</p>
      <a className="button primary" href="/auth/login">
        {t("signIn")}
      </a>
    </section>
  );
}

// NoMembership: signed in, but no company has added this person yet.
export function NoMembership() {
  const { t } = useT();
  const { signOut } = useSessionActions();
  return (
    <section className="card narrow">
      <h2>{t("noMembershipTitle")}</h2>
      <p>{t("noMembership")}</p>
      <button type="button" onClick={() => void signOut()}>
        {t("signOut")}
      </button>
    </section>
  );
}
