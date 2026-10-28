import { useEffect, useEffectEvent } from "react";
import {
  hashKey,
  useQueryClient,
  type Query,
  type QueryKey,
} from "@tanstack/react-query";
import { ApiError } from "@/api/errors";

const defaultDenial = (error: unknown) =>
  error instanceof ApiError && [401, 403, 404].includes(error.status);

// Retire authority when the cache records a denial, before a late successful
// mutation can replace that evidence. Watching the cache also covers refetches
// initiated outside the current component.
export function useQueryAccessDenial(
  keys: readonly QueryKey[],
  onDenied: () => void,
  principal?: { key: QueryKey; actor: string },
  isDenied: (error: unknown) => boolean = defaultDenial,
) {
  const client = useQueryClient();
  const identities = JSON.stringify(keys.map(hashKey));
  const principalIdentity = principal ? hashKey(principal.key) : undefined;
  const actor = principal?.actor;
  const notify = useEffectEvent(onDenied);
  useEffect(() => {
    const watched = new Set<string>(JSON.parse(identities));
    const cache = client.getQueryCache();
    let retired = false;
    const inspect = (query: Query) => {
      if (retired || !watched.has(hashKey(query.queryKey))) return;
      const error = query.state.error;
      let denied = query.state.status === "error" && isDenied(error);
      if (hashKey(query.queryKey) === principalIdentity) {
        const identity = query.state.data as
          | { provider?: string; id?: string }
          | undefined;
        denied ||=
          query.state.status === "error" ||
          (query.state.status === "success" &&
            JSON.stringify([identity?.provider, identity?.id]) !== actor);
      }
      if (denied) {
        retired = true;
        notify();
      }
    };
    const unsubscribe = cache.subscribe((event) => {
      if (
        event.type === "updated" &&
        (event.action.type === "error" || event.action.type === "success")
      ) {
        inspect(event.query);
      }
    });
    cache.getAll().forEach(inspect);
    return unsubscribe;
  }, [client, identities, principalIdentity, actor, isDenied]);
}
