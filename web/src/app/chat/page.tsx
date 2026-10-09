import { DashboardChat } from "@/app/dashboard/dashboard-chat";
import { getTranslations } from "@/lib/i18n/server";
import styles from "./chat.module.css";

export async function generateMetadata() {
  const t = await getTranslations();
  return { title: t("Chat") };
}

export default function ChatPage() {
  return (
    <main className={styles.page}>
      <div className={styles.previewNotice} role="status">
        <span aria-hidden="true">●</span> Chat Preview · Features are under acceptance testing. Review changes before using Live Project.
      </div>
      <DashboardChat />
    </main>
  );
}
