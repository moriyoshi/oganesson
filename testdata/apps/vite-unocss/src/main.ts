import "virtual:uno.css";
import { title } from "./view.tsx";

// UnoCSS extracts utilities from the modules its pipeline includes
// (.html, .[jt]sx, .vue, …; not plain .ts) and from index.html.
document.getElementById("app")!.innerHTML = title;
