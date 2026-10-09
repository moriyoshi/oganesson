import raw from "./note.txt?raw";
import logoUrl from "./logo.png?url";
import iconInline from "./icon.svg?inline";
import SquareWorker from "./square.worker.ts?worker";
import counts from "virtual:import-counts";
import { nanoid } from "nanoid";
import { Thing } from "./decorated";

const out = document.getElementById("out")!;
function show(id: string, value: string) {
  const li = document.createElement("li");
  li.id = id;
  li.textContent = value;
  out.append(li);
}

show("raw", raw.trim());
show("url", logoUrl);
show("inline", iconInline.slice(0, 30));
show("asset", new URL("./asset.txt", import.meta.url).pathname);
show("nanoid", String(nanoid(8).length));
show("counts", String(counts.main));
const thing = new Thing() as Thing & { label: string };
show("decorated", `${thing.name}:${thing.label}`);

const w = new Worker(new URL("./worker.ts", import.meta.url), { type: "module" });
w.onmessage = (e) => show("worker", String(e.data));
w.postMessage(21);

const sq = new SquareWorker();
sq.onmessage = (e) => show("square", String(e.data));
sq.postMessage(7);

fetch(new URL("./asset.txt", import.meta.url))
  .then((r) => r.text())
  .then((t) => show("fetched", t.trim()));
