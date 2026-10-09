import type { NextPageContext } from "next";

function Legacy({ fromInitial }: { fromInitial: string }) {
  // typeof window is a constant per build, as Next's SWC makes it: each
  // bundle keeps one branch.
  return (
    <>
      <p id="legacy">{fromInitial}</p>
      <p id="where">{typeof window === "undefined" ? "og-where-server" : "og-where-browser"}</p>
    </>
  );
}

// ctx.res starts at status 200, as Node's ServerResponse does under next
// start (next-graphql-react waits for it).
Legacy.getInitialProps = async (ctx: NextPageContext) => ({ fromInitial: `gip ${ctx.query.q ?? "none"} ${ctx.res ? ctx.res.statusCode : "no res"}` });

export default Legacy;
