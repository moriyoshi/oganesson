import styles from "./widget.module.css";

export default function Banner({ text }: { text: string }) {
  return <p id="banner" className={styles.widget}>banner {text}</p>;
}
