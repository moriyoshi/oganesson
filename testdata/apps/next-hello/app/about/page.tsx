import "./tailwind.css";

export const metadata = { title: "About" };

export default function About() {
  return (
    <>
      <p id="about">A static page, prerendered at build time.</p>
      <p id="tw" className="about-note underline tracking-widest">
        Styled with Tailwind.
      </p>
    </>
  );
}
