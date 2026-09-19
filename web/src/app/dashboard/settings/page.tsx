import { getTranslations } from "@/lib/i18n/server";
import dashboard from "../dashboard.module.css";
import { DecisionSettingsLive } from "./decision-settings-live";
import { RuntimeSettingsLive } from "./runtime-settings-live";
import { SettingsHub } from "./settings-hub";
import styles from "./runtime-settings.module.css";

export default async function SettingsPage() {
  const t = await getTranslations();
  return (
    <section className={dashboard.content}>
      <div className={styles.page}>
        <h1 className="sr-only">{t("Settings")}</h1>
        <SettingsHub />
        <DecisionSettingsLive />
        <div className={styles.runtimeHeading} id="runtime">
          <h2>{t("Runtime settings")}</h2>
        </div>
        <RuntimeSettingsLive />
      </div>
    </section>
  );
}
