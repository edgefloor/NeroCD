import { afterEach, expect, test, vi } from "vitest";
import { eligibleSourceGrant, listHostingSourceGrants, newSourceGrantRequestID, requestHostingSourceGrant, revokeHostingSourceGrant, sourceGrantsQuery } from "./hosting-source-grants";
import type { Resource } from "./hosting";
const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "env", name: "Private", kind: "application", generation: 2, created_by: "user", created_at: "", updated_at: "", source: { kind: "git", git: { source_id: "source", generation: 3, credential_kind: "https_basic", repository_url: "https://example.com/repo", requested_ref: "main", settings: { strategy: "dockerfile" } } } };
const grant = { id: "grant", resource_id: "resource", source_id: "source", source_generation: 3, version: 1, kind: "https_basic" as const, revoked: false };
const json = (body: unknown) => new Response(JSON.stringify(body));
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
test("request IDs use sixteen cryptographically random bytes encoded as lower hex", () => { const random = vi.spyOn(crypto, "getRandomValues"); expect(newSourceGrantRequestID()).toMatch(/^[0-9a-f]{32}$/); expect(random).toHaveBeenCalledOnce(); expect(random.mock.calls[0]![0]).toBeInstanceOf(Uint8Array); expect(random.mock.calls[0]![0]!.byteLength).toBe(16); });
test("grant writes whitelist references, encode targets and revoke with exactly an empty object", async () => {
 const fetch = vi.fn(async (_url: string, _options: RequestInit) => json({ grant, revoked: true })); vi.stubGlobal("fetch", fetch); const signal = new AbortController().signal;
 const input = { expected_resource_generation: 2, expected_source_generation: 3, secret_id: "secret", secret_version: 4, request_id: "0123456789abcdef0123456789abcdef", credential: "forbidden" };
 await requestHostingSourceGrant("resource/one", input, { signal }); await requestHostingSourceGrant("resource/one", input, { signal }); await revokeHostingSourceGrant("resource/one", "grant/one", { signal });
 expect(fetch.mock.calls[0]).toEqual(["/api/v1/resources/resource%2Fone/source-grants", expect.objectContaining({ method: "POST", signal, credentials: "include", body: '{"expected_resource_generation":2,"expected_source_generation":3,"secret_id":"secret","secret_version":4,"request_id":"0123456789abcdef0123456789abcdef"}' })]); expect(fetch.mock.calls[1]![1].body).toBe(fetch.mock.calls[0]![1].body); expect(fetch.mock.calls[2]).toEqual(["/api/v1/resources/resource%2Fone/source-grants/grant%2Fone/revoke", expect.objectContaining({ method: "POST", body: "{}" })]);
});
test("grant pages retain only reference metadata and reject foreign resource scope", async () => {
 const fetch = vi.fn(async (_url: string, _options: RequestInit) => json({ items: [{ ...grant, credential: "secret-value", internal_policy: "private" }], count: 1, total: 1, limit: 20, offset: 20 })); vi.stubGlobal("fetch", fetch);
 const query = sourceGrantsQuery("actor", "project", "resource", 20); const result = await query.queryFn!({ signal: new AbortController().signal } as never); expect(query.queryKey).toEqual(["hostingSourceGrants", "actor", "project", "resource", 20]); expect(JSON.stringify(result)).not.toMatch(/credential|internal_policy|secret-value/); expect(fetch.mock.calls[0]![0]).toBe("/api/v1/resources/resource/source-grants?limit=20&offset=20");
 vi.stubGlobal("fetch", vi.fn(async () => json({ items: [{ ...grant, resource_id: "foreign" }] }))); await expect(listHostingSourceGrants("resource", 0)).rejects.toThrow("scope mismatch");
});
test.each([{ source_generation: 0 }, { source_generation: 2 }, { source_id: "old" }, { resource_id: "other" }, { version: 2 }, { revoked: true }])("only exact nonrevoked v1 grants with generation evidence are eligible: %j", (change) => { expect(eligibleSourceGrant(grant, resource)).toBe(true); expect(eligibleSourceGrant({ ...grant, ...change }, resource)).toBe(false); });
