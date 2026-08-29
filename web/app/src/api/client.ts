import { ApiError } from "./errors";

export type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  query?: object;
  signal?: AbortSignal;
  cache?: RequestCache;
  /** A caller-owned, safe request identity for durable mutation replay. */
  requestID?: string;
};

function withQuery(path: string, query: RequestOptions["query"]): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined) params.set(key, String(value));
  }
  const encoded = params.toString();
  return encoded ? `${path}?${encoded}` : path;
}

async function errorFor(response: Response): Promise<ApiError> {
  let message = `${response.status} ${response.statusText}`.trim();
  try {
    const body = (await response.json()) as { error?: unknown };
    if (typeof body.error === "string" && body.error) message = body.error;
  } catch {
    // Non-JSON errors retain their HTTP status message.
  }
  return new ApiError(response.status, message, response.headers.get("X-Request-ID"));
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? "GET";
  const hasBody = options.body !== undefined;
  const headers: Record<string, string> = { Accept: "application/json" };
  if (hasBody) headers["Content-Type"] = "application/json";
  if (method === "POST" || method === "PUT" || method === "PATCH" || method === "DELETE") headers["X-NeroCD-CSRF"] = "1";
  if (options.requestID) headers["X-Request-ID"] = options.requestID;

  const response = await fetch(withQuery(path, options.query), {
    method,
    headers,
    credentials: "include",
    body: hasBody ? JSON.stringify(options.body) : undefined,
    signal: options.signal,
    cache: options.cache,
  });
  if (!response.ok) throw await errorFor(response);
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}
