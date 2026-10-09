// Incremental static regeneration: rebuilt at most once per second.
export const revalidate = 1;

export default function Time() {
  return <p id="time">{String(Date.now())}</p>;
}
