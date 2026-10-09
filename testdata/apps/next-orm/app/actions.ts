"use server";

import { revalidatePath } from "next/cache";
import { open, notes } from "@/lib/db";

export async function addNote(form: FormData) {
  const { db, sqlite } = open();
  try {
    db.insert(notes).values({ title: String(form.get("title") ?? ""), body: String(form.get("body") ?? "") || null }).run();
  } finally {
    sqlite.close();
  }
  revalidatePath("/");
}
