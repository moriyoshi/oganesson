"use server";
import { cookies } from "next/headers";
import { revalidatePath } from "next/cache";

export async function like() {
  const jar = await cookies();
  const n = Number(jar.get("likes")?.value ?? 0) + 1;
  jar.set("likes", String(n));
  revalidatePath("/");
}

export async function shout(text: string) {
  return { text: text.toUpperCase(), at: "server" };
}
