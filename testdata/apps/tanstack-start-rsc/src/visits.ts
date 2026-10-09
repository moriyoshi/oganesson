import { createServerFn } from "@tanstack/react-start";
import { bump } from "./visitStore";

// A mutation, as TanStack Start does them with RSC: an explicit POST
// server function (React's "use server" actions are not part of Start).
// The client build keeps only the RPC stub, so visitStore stays out of it.
export const bumpVisits = createServerFn({ method: "POST" }).handler(() => bump());
