import { defineConfig } from "vite";
import UnoCSS from "unocss/vite";
import { presetWind3 } from "unocss";

export default defineConfig({ plugins: [UnoCSS({ presets: [presetWind3()] })] });
