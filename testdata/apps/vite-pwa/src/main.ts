import { registerSW } from "virtual:pwa-register";

registerSW({ immediate: true, onRegistered: () => (document.getElementById("status")!.textContent = "registered") });
