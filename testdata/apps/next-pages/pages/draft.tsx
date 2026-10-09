import type { GetStaticProps } from "next";

export const getStaticProps: GetStaticProps<{ draft: boolean; preview: string; builtAt: number }> = async (ctx) => ({
  props: { draft: Boolean(ctx.draftMode), preview: ctx.preview ? JSON.stringify(ctx.previewData ?? null) : "none", builtAt: Date.now() },
});

export default function Draft({ draft, preview, builtAt }: { draft: boolean; preview: string; builtAt: number }) {
  return (
    <p id="draft" data-built={builtAt}>
      draft={String(draft)} preview={preview}
    </p>
  );
}
