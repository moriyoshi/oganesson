// experimental.globalNotFound: a full-document 404 that replaces the root
// layout + not-found pair for unmatched routes (Next 16.3 App Router).
export default function GlobalNotFound() {
  return (
    <html lang="en">
      <body>
        <h1 id="gnf">Global 404</h1>
      </body>
    </html>
  );
}
