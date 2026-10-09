import LegacyClient from "../legacy-client";
import { Widget } from "../../../lib/cjs-client";

// A dynamic segment without generateStaticParams: nothing to prerender at
// build time; each tag renders on its first request.
export default async function Tag({ params }: { params: Promise<{ tag: string }> }) {
  const { tag } = await params;
  return (
    <>
      <p id="tag">tag {tag}</p>
      <LegacyClient />
      <Widget />
    </>
  );
}
