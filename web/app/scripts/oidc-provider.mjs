import { createHash, generateKeyPairSync, randomBytes, sign } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";

const [appOrigin, infoPath, secretPath] = process.argv.slice(2);
if (!appOrigin || !infoPath || !secretPath) throw new Error("app origin, provider info path, and client secret path are required");
const parsedAppOrigin = new URL(appOrigin);
if (parsedAppOrigin.protocol !== "http:" || !["127.0.0.1", "localhost", "::1"].includes(parsedAppOrigin.hostname)) throw new Error("browser fixture app origin must be loopback HTTP");
const clientSecret = readFileSync(secretPath, "utf8").trim();
if (!clientSecret) throw new Error("browser fixture client secret is empty");

const clientID = "nerocd-browser";
const subject = "nerocd-browser-oidc-subject";
const redirectURI = `${parsedAppOrigin.origin}/api/v1/oidc/callback`;
const keyID = "browser-fixture-key";
const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
const publicJWK = publicKey.export({ format: "jwk" });
const codes = new Map();
let issuer = "";

function json(response, status, body) {
  const encoded = JSON.stringify(body);
  response.writeHead(status, { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(encoded), "Cache-Control": "no-store" });
  response.end(encoded);
}

function base64urlJSON(value) {
  return Buffer.from(JSON.stringify(value)).toString("base64url");
}

function idToken(nonce) {
  const now = Math.floor(Date.now() / 1000);
  const header = base64urlJSON({ alg: "RS256", kid: keyID, typ: "JWT" });
  const payload = base64urlJSON({ iss: issuer, sub: subject, aud: clientID, exp: now + 300, iat: now, nonce });
  const signingInput = `${header}.${payload}`;
  const signature = sign("RSA-SHA256", Buffer.from(signingInput), privateKey).toString("base64url");
  return `${signingInput}.${signature}`;
}

async function readBody(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 64 * 1024) throw new Error("request too large");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

const server = createServer(async (request, response) => {
  try {
    const requestURL = new URL(request.url ?? "/", issuer || "http://127.0.0.1");
    if (request.method === "GET" && requestURL.pathname === "/.well-known/openid-configuration") {
      return json(response, 200, {
        issuer,
        authorization_endpoint: `${issuer}/authorize`,
        token_endpoint: `${issuer}/token`,
        jwks_uri: `${issuer}/jwks`,
        response_types_supported: ["code"],
        subject_types_supported: ["public"],
        id_token_signing_alg_values_supported: ["RS256"],
        code_challenge_methods_supported: ["S256"],
      });
    }
    if (request.method === "GET" && requestURL.pathname === "/jwks") {
      return json(response, 200, { keys: [{ ...publicJWK, alg: "RS256", kid: keyID, use: "sig" }] });
    }
    if (request.method === "GET" && requestURL.pathname === "/authorize") {
      if ((request.headers.cookie ?? "").includes("nerocd_oidc_flow")) return json(response, 400, { error: "cookie_leak" });
      const required = ["client_id", "redirect_uri", "state", "nonce", "code_challenge", "code_challenge_method", "response_type"];
      if (required.some((name) => requestURL.searchParams.getAll(name).length !== 1)) return json(response, 400, { error: "invalid_request" });
      if (requestURL.searchParams.get("client_id") !== clientID || requestURL.searchParams.get("redirect_uri") !== redirectURI || requestURL.searchParams.get("response_type") !== "code" || requestURL.searchParams.get("code_challenge_method") !== "S256") return json(response, 400, { error: "invalid_request" });
      const code = randomBytes(24).toString("base64url");
      codes.set(code, { challenge: requestURL.searchParams.get("code_challenge"), nonce: requestURL.searchParams.get("nonce") });
      const callback = new URL(redirectURI);
      callback.searchParams.set("code", code);
      callback.searchParams.set("state", requestURL.searchParams.get("state"));
      callback.searchParams.set("iss", issuer);
      callback.searchParams.set("session_state", "ignored-browser-fixture-hint");
      response.writeHead(302, { Location: callback.toString(), "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" });
      return response.end();
    }
    if (request.method === "POST" && requestURL.pathname === "/token") {
      const form = new URLSearchParams(await readBody(request));
      const authorization = request.headers.authorization ?? "";
      let presentedID = form.get("client_id") ?? "";
      let presentedSecret = form.get("client_secret") ?? "";
      if (authorization.startsWith("Basic ")) {
        const decoded = Buffer.from(authorization.slice(6), "base64").toString("utf8");
        const separator = decoded.indexOf(":");
        presentedID = decodeURIComponent(decoded.slice(0, separator));
        presentedSecret = decodeURIComponent(decoded.slice(separator + 1));
      }
      const code = form.get("code") ?? "";
      const record = codes.get(code);
      const verifier = form.get("code_verifier") ?? "";
      const challenge = createHash("sha256").update(verifier).digest("base64url");
      if (!record || presentedID !== clientID || presentedSecret !== clientSecret || form.get("grant_type") !== "authorization_code" || form.get("redirect_uri") !== redirectURI || challenge !== record.challenge) return json(response, 400, { error: "invalid_grant" });
      codes.delete(code);
      return json(response, 200, { access_token: randomBytes(24).toString("base64url"), token_type: "Bearer", expires_in: 300, id_token: idToken(record.nonce) });
    }
    return json(response, 404, { error: "not_found" });
  } catch {
    return json(response, 400, { error: "invalid_request" });
  }
});

server.listen(0, "localhost", () => {
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("fixture provider address unavailable");
  issuer = `http://localhost:${address.port}`;
  writeFileSync(infoPath, JSON.stringify({ issuer, clientID, subject }), { mode: 0o600 });
});

for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => server.close(() => process.exit(0)));
