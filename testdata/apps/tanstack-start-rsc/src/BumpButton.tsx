"use client";
import { useRouter } from "@tanstack/react-router";
import { bumpVisits } from "./visits";

// Calls the mutation, then invalidates the route so the server component
// renders again with the new count.
export function BumpButton() {
  const router = useRouter();
  return (
    <button
      id="bump"
      onClick={async () => {
        await bumpVisits();
        await router.invalidate();
      }}
    >
      bump
    </button>
  );
}
