import { fail } from "@sveltejs/kit";

export function load({ url, cookies }) {
  return {
    greeting: `hello ${url.searchParams.get("q") ?? "nobody"}`,
    visits: Number(cookies.get("visits") ?? 0),
  };
}

export const actions = {
  visit: async ({ request, cookies }) => {
    const form = await request.formData();
    const by = Number(form.get("by") ?? 1);
    if (!Number.isFinite(by)) return fail(400, { error: "by must be a number" });
    const visits = Number(cookies.get("visits") ?? 0) + by;
    cookies.set("visits", String(visits), { path: "/" });
    return { visits };
  },
};
