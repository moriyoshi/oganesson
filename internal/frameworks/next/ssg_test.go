package next

import (
	"strings"
	"testing"
)

func TestSSGTransform(t *testing.T) {
	out, err := ssgTransform("/p/pages/x.tsx", `import { db } from "./db";
import { format, unusedAnyway } from "./fmt";
import type { GetServerSideProps } from "next";
import "./side-effect.css";
const helper = () => db.query();
const unrelated = 1;
export const getServerSideProps: GetServerSideProps = async () => ({ props: { x: helper() } });
export default function Page({ x }: { x: string }) { return format(x); }
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"./db", "helper", "getServerSideProps:", "db.query"} {
		if strings.Contains(out, gone) {
			t.Errorf("output still has %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{`import { format, unusedAnyway } from "./fmt";`, `import "./side-effect.css";`, "const unrelated = 1;", "export default function Page", "export var __N_SSP = true;", "import type { GetServerSideProps }"} {
		if !strings.Contains(out, kept) {
			t.Errorf("output lacks %q:\n%s", kept, out)
		}
	}

	out, err = ssgTransform("/p/pages/y.js", `import fs from "fs";
import { slugify } from "./util";
async function loadPaths() { return fs.readdirSync("x").map(slugify); }
export async function getStaticProps() { return { props: {} }; }
export const revalidateHint = 1, getStaticPaths = async () => ({ paths: await loadPaths(), fallback: false });
export { loadPaths as getStaticPathsAlias };
export default function Y() { return null; }
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"getStaticProps", "getStaticPaths ="} {
		if strings.Contains(out, gone) {
			t.Errorf("output still has %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"export const revalidateHint = 1;", "export var __N_SSG = true;", "async function loadPaths()", `import fs from "fs";`} {
		if !strings.Contains(out, kept) {
			t.Errorf("output lacks %q:\n%s", kept, out)
		}
	}

	// export { local as getStaticProps }: the clause goes, then the local.
	out, err = ssgTransform("/p/pages/z.js", "import data from \"./data\";\nconst gsp = async () => ({ props: data });\nexport { gsp as getStaticProps };\nexport default function Z() { return 1; }\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "gsp") || strings.Contains(out, "./data") || !strings.Contains(out, "__N_SSG") {
		t.Errorf("export clause:\n%s", out)
	}

	if _, err := ssgTransform("/p/pages/w.js", "export function getStaticProps() {}\nexport function getServerSideProps() {}\nexport default () => null;\n"); err == nil || !strings.Contains(err.Error(), "can not use getStaticProps") {
		t.Errorf("both data functions: err = %v", err)
	}
	if out, _ := ssgTransform("/p/pages/v.js", "export default function V() { return 'getStaticProps is just text'; }\n"); strings.Contains(out, "__N_") {
		t.Errorf("no data exports, but flagged:\n%s", out)
	}
}
