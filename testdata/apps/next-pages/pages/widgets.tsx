// Components named at run time through a computed require, as Agility
// CMS's example picks its modules (a webpack context module in Next).
import type { GetServerSideProps } from "next";
import { describe } from "../lib/browserpkg";
import { describeRenderer } from "../lib/missingexport/index.mjs";
// node:url in the browser too: Next aliases it to its native-url there.
import { parse } from "url";

export const getServerSideProps: GetServerSideProps<{ names: string[] }> = async () => ({
  props: { names: ["banner", "nested/badge"] },
});

export default function Widgets({ names }: { names: string[] }) {
  return (
    <div>
      <p id="browserpkg">{describe()}</p>
      <p id="missing-export">{describeRenderer()}</p>
      <p id="url">{parse("/a?b=1").pathname}</p>
      {names.map((name) => {
        const Widget = require(`../widgets/${name}.tsx`).default;
        return <Widget key={name} text={name} />;
      })}
    </div>
  );
}
