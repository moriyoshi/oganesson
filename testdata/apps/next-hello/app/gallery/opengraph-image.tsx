import { ImageResponse } from "next/og";

export const size = { width: 120, height: 63 };
export const contentType = "image/png";
export const alt = "The gallery";

export default function Image() {
  return new ImageResponse(
    (
      <div style={{ width: "100%", height: "100%", display: "flex", alignItems: "center", justifyContent: "center", background: "#7928ca", color: "white", fontSize: 28 }}>
        OG
      </div>
    ),
    size,
  );
}
