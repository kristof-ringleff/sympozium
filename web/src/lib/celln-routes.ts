// Matching a model against a route (an Agent's own connection: exact models,
// or any model), as the resolver does for the connection's model list.
// Pure (a type import only) so run diagnosis can use it in isolation.
import type { CellnMediatedRoute } from "@/lib/api";

/**
 * A model name the API accepts (api ValidModelIdentifier, held a little
 * tighter for typed input): 1–128 bytes, no whitespace or control characters,
 * and not the any-model token itself.
 */
export function validModelName(model: string): boolean {
  const bytes = new TextEncoder().encode(model).length;
  return bytes >= 1 && bytes <= 128 && model !== "*" && !/[\s\u0000-\u001f\u007f]/.test(model);
}

/** Whether a route admits a model: any valid name on an any-model route, else one of its exact names. */
export function routeAllowsModel(route: Pick<CellnMediatedRoute, "models" | "anyModel">, model: string): boolean {
  return route.anyModel ? validModelName(model) : route.models.includes(model);
}

/** The route's models for people: the names, or "any model". */
export function routeModelsLabel(route: Pick<CellnMediatedRoute, "models" | "anyModel">): string {
  return route.anyModel ? "any model" : route.models.join(", ");
}

