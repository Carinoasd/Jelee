// Pure logic of the developer mode API console (G49.4): reading the running
// instance's OpenAPI document, keeping direct-delivery routes out, building
// request URLs, rendering a credential-free cURL command and masking
// credentials in what the console shows. No Vue and no network here.

export const consoleMethods = ["get", "head", "post", "put", "patch", "delete"] as const;
export type ConsoleMethod = (typeof consoleMethods)[number];

export type ParamLocation = "path" | "query" | "header";

export interface ConsoleParam {
  readonly name: string;
  readonly in: ParamLocation;
  readonly required: boolean;
  readonly description: string;
  /** JSON schema type of the value ("string", "integer", "boolean", ...). */
  readonly type: string;
  /** Allowed values; empty when free-form. */
  readonly options: readonly string[];
}

export interface ConsoleOperation {
  /** "METHOD /path", unique per document. */
  readonly id: string;
  readonly method: ConsoleMethod;
  readonly path: string;
  readonly summary: string;
  readonly params: readonly ConsoleParam[];
  readonly hasBody: boolean;
  readonly bodyRequired: boolean;
  /** Formatted JSON skeleton derived from the request body schema. */
  readonly bodyTemplate: string;
  /** Marked x-jelee-dev-only by a capable instance. */
  readonly devOnly: boolean;
  /** Changes state: needs the two-step confirmation before it is sent. */
  readonly write: boolean;
  /** A dangerous operation: also needs an explicit acknowledgement. */
  readonly dangerous: boolean;
}

/**
 * Path segments of playback and direct-delivery routes, matching the
 * no-playback gate (scripts/check-no-playback.mjs). The web client must not
 * call or show the source stream or its subtitle and audio tracks (G27.3).
 */
const deliverySegment = /^(?:play|player|playback|playing|now-playing|stream|streams|subtitles|audio|cast|pip|picture-in-picture|theater)$/i;
/** Every route under the sources prefix is direct delivery for native clients. */
const sourcesPrefix = /^\/api\/v1\/sources(?:\/|$)/i;
/**
 * Routes the console never offers although they are not delivery: the
 * developer mode enable token is issued to loopback callers for the CLI only.
 */
const hiddenRoutes = new Set(["/api/v1/dev/token"]);

/** Whether a path (template or concrete) is a playback or direct-delivery route. */
export function isDeliveryPath(path: string): boolean {
  const pathname = path.split(/[?#]/)[0] ?? "";
  if (sourcesPrefix.test(pathname)) {
    return true;
  }
  return pathname.split("/").some((segment) => deliverySegment.test(safeDecode(segment)));
}

/** Whether the console must refuse to list or call this path. */
export function isExcludedPath(path: string): boolean {
  return isDeliveryPath(path) || hiddenRoutes.has(path.split(/[?#]/)[0] ?? "");
}

function safeDecode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/**
 * Paths whose operations are dangerous beyond being writes (G45.6): they
 * discard data, end sessions, replace settings or switch developer mode.
 */
const dangerousPaths: readonly RegExp[] = [
  /\/probe\/rebuild$/,
  /^\/api\/v1\/site\/(?:import|appearance\/reset|plugins\/reset)$/,
  /^\/api\/v1\/dev(?:\/|$)/,
  /^\/api\/v1\/auth\/(?:logout|rotate)$/,
  /^\/api\/v1\/users\/me\/(?:password|two-factor\/disable)$/,
  /\/rotate-secret$/,
  /^\/api\/v1\/client-control\/(?:policy|clients\/\{id\}\/(?:block|kick))$/,
  /^\/api\/v1\/access\/policy$/,
];

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
type JsonObject = { [key: string]: Json };

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/** Resolves a local "#/components/..." reference; anything else stays as is. */
function resolve(document: JsonObject, value: unknown, depth = 0): JsonObject | null {
  if (!isObject(value)) {
    return null;
  }
  const ref = value.$ref;
  if (typeof ref !== "string" || depth > 16) {
    return value;
  }
  if (!ref.startsWith("#/")) {
    return null;
  }
  let target: unknown = document;
  for (const part of ref.slice(2).split("/")) {
    target = isObject(target) ? target[part.replace(/~1/g, "/").replace(/~0/g, "~")] : undefined;
  }
  return resolve(document, target, depth + 1);
}

function schemaType(schema: JsonObject | null): string {
  if (schema === null) {
    return "string";
  }
  const type = schema.type;
  if (typeof type === "string") {
    return type;
  }
  if (Array.isArray(type)) {
    return text(type.find((entry) => entry !== "null")) || "string";
  }
  if (isObject(schema.properties)) {
    return "object";
  }
  return "string";
}

function enumValues(schema: JsonObject | null): string[] {
  const values = schema?.enum;
  if (!Array.isArray(values)) {
    return schemaType(schema) === "boolean" ? ["true", "false"] : [];
  }
  return values.filter((value) => value !== null).map((value) => (typeof value === "object" ? JSON.stringify(value) : String(value)));
}

/** Builds an example value of a schema: defaults, first enum values, zeros. */
export function exampleValue(document: JsonObject, value: unknown, depth = 0): Json {
  const schema = resolve(document, value);
  if (schema === null || depth > 6) {
    return null;
  }
  if (schema.default !== undefined) {
    return schema.default;
  }
  if (Array.isArray(schema.enum) && schema.enum.length > 0) {
    return schema.enum.find((entry) => entry !== null) ?? null;
  }
  for (const combinator of ["oneOf", "anyOf"] as const) {
    const options = schema[combinator];
    if (Array.isArray(options) && options.length > 0) {
      const chosen = options.find((option) => schemaType(resolve(document, option)) !== "null") ?? options[0];
      return exampleValue(document, chosen, depth + 1);
    }
  }
  if (Array.isArray(schema.allOf)) {
    const merged: JsonObject = {};
    for (const part of schema.allOf) {
      const example = exampleValue(document, part, depth + 1);
      if (isObject(example)) {
        Object.assign(merged, example);
      }
    }
    return merged;
  }
  switch (schemaType(schema)) {
    case "object": {
      const result: JsonObject = {};
      const properties = isObject(schema.properties) ? schema.properties : {};
      for (const [name, property] of Object.entries(properties)) {
        // The acknowledgement of a dangerous operation is never pre-filled.
        result[name] = name === "iUnderstand" ? false : exampleValue(document, property, depth + 1);
      }
      return result;
    }
    case "array":
      return [];
    case "integer":
    case "number":
      return typeof schema.minimum === "number" ? schema.minimum : 0;
    case "boolean":
      return false;
    case "null":
      return null;
    default:
      return "";
  }
}

function hasProperty(document: JsonObject, value: unknown, name: string, depth = 0): boolean {
  const schema = resolve(document, value);
  if (schema === null || depth > 6) {
    return false;
  }
  if (isObject(schema.properties) && name in schema.properties) {
    return true;
  }
  return ["allOf", "oneOf", "anyOf"].some((key) => {
    const parts = schema[key];
    return Array.isArray(parts) && parts.some((part) => hasProperty(document, part, name, depth + 1));
  });
}

/**
 * Header parameters the console may set. Credentials (Cookie, Authorization,
 * CSRF, the setup token) are never typed in: the session the page already
 * holds is the only credential the console uses.
 */
const allowedHeaders = new Set(["idempotency-key", "if-none-match", "if-match"]);

function readParams(document: JsonObject, path: string, pathItem: JsonObject, operation: JsonObject): ConsoleParam[] {
  const params = new Map<string, ConsoleParam>();
  const lists = [pathItem.parameters, operation.parameters];
  for (const list of lists) {
    if (!Array.isArray(list)) {
      continue;
    }
    for (const entry of list) {
      const param = resolve(document, entry);
      const location = text(param?.in);
      const name = text(param?.name);
      if (param === null || name === "" || (location !== "path" && location !== "query" && location !== "header")) {
        continue;
      }
      if (location === "header" && !allowedHeaders.has(name.toLowerCase())) {
        continue;
      }
      const schema = resolve(document, param.schema);
      params.set(location + ":" + name, {
        name,
        in: location,
        required: location === "path" || param.required === true,
        description: text(param.description),
        type: schemaType(schema),
        options: enumValues(schema),
      });
    }
  }
  // A template segment without a declared parameter still needs a value.
  for (const match of path.matchAll(/\{([^}]+)\}/g)) {
    const name = match[1] ?? "";
    if (!params.has("path:" + name)) {
      params.set("path:" + name, { name, in: "path", required: true, description: "", type: "string", options: [] });
    }
  }
  const order: Record<ParamLocation, number> = { path: 0, query: 1, header: 2 };
  return [...params.values()].sort((a, b) => order[a.in] - order[b.in]);
}

/**
 * Lists the operations of an OpenAPI document that the console may call,
 * sorted by path and method. Playback and direct-delivery routes and the
 * loopback-only token route are left out.
 */
export function readOperations(spec: unknown): ConsoleOperation[] {
  if (!isObject(spec) || !isObject(spec.paths)) {
    return [];
  }
  const operations: ConsoleOperation[] = [];
  for (const [path, rawItem] of Object.entries(spec.paths)) {
    const pathItem = resolve(spec, rawItem);
    if (pathItem === null || !path.startsWith("/") || isExcludedPath(path)) {
      continue;
    }
    for (const method of consoleMethods) {
      const operation = pathItem[method];
      if (!isObject(operation)) {
        continue;
      }
      const body = resolve(spec, operation.requestBody);
      const content = isObject(body?.content) ? body.content : {};
      const jsonBody = isObject(content["application/json"]) ? content["application/json"] : null;
      const bodySchema = jsonBody?.schema;
      const write = method !== "get" && method !== "head";
      const devOnly = operation["x-jelee-dev-only"] === true;
      operations.push({
        id: method.toUpperCase() + " " + path,
        method,
        path,
        summary: text(operation.summary),
        params: readParams(spec, path, pathItem, operation),
        hasBody: jsonBody !== null,
        bodyRequired: body?.required === true,
        bodyTemplate: jsonBody === null ? "" : JSON.stringify(exampleValue(spec, bodySchema), null, 2),
        devOnly,
        write,
        dangerous:
          write &&
          (method === "delete" || devOnly || dangerousPaths.some((pattern) => pattern.test(path)) || hasProperty(spec, bodySchema, "iUnderstand")),
      });
    }
  }
  return operations.sort((a, b) => a.path.localeCompare(b.path) || consoleMethods.indexOf(a.method) - consoleMethods.indexOf(b.method));
}

export type ParamValues = Readonly<Record<string, string>>;

/** Form key of a parameter; path and query parameters may share a name. */
export function paramKey(param: ConsoleParam): string {
  return param.in + ":" + param.name;
}

export type RequestProblem = { kind: "missing"; name: string } | { kind: "segment"; name: string } | { kind: "excluded" } | { kind: "body" };

export interface BuiltRequest {
  /** Concrete path with encoded parameter values. */
  readonly path: string;
  readonly query: Readonly<Record<string, string>>;
  readonly headers: Readonly<Record<string, string>>;
  /** Parsed JSON body; undefined when nothing is sent. */
  readonly body: Json | undefined;
  /** The body text as sent (compact JSON); empty when none. */
  readonly bodyText: string;
}

/**
 * Turns form values into a request, or the first problem found. Path values
 * are encoded as one segment each, and "." and ".." are refused, so a value
 * can never move the request onto another route.
 */
export function buildRequest(operation: ConsoleOperation, values: ParamValues, bodyText: string): BuiltRequest | RequestProblem {
  let path = operation.path;
  const query: Record<string, string> = {};
  const headers: Record<string, string> = {};
  for (const param of operation.params) {
    const value = values[paramKey(param)] ?? "";
    if (value === "") {
      if (param.required) {
        return { kind: "missing", name: param.name };
      }
      continue;
    }
    if (param.in === "path") {
      if (value === "." || value === "..") {
        return { kind: "segment", name: param.name };
      }
      path = path.split("{" + param.name + "}").join(encodeURIComponent(value));
    } else if (param.in === "query") {
      query[param.name] = value;
    } else {
      headers[param.name] = value;
    }
  }
  if (isExcludedPath(path) || isExcludedPath(operation.path)) {
    return { kind: "excluded" };
  }
  let body: Json | undefined;
  let compact = "";
  if (operation.hasBody && bodyText.trim() !== "") {
    try {
      body = JSON.parse(bodyText) as Json;
    } catch {
      return { kind: "body" };
    }
    compact = JSON.stringify(body);
  } else if (operation.hasBody && operation.bodyRequired) {
    return { kind: "body" };
  }
  return { path, query, headers, body, bodyText: compact };
}

/** The query string of a built request, with a leading "?" when not empty. */
export function queryString(query: Readonly<Record<string, string>>): string {
  const search = new URLSearchParams(query).toString();
  return search === "" ? "" : "?" + search;
}

/** Placeholders the copied command carries instead of the session credentials. */
export const sessionCookiePlaceholder = "__Host-jelee_session=<SESSION_COOKIE>";
export const csrfPlaceholder = "<CSRF_TOKEN>";
export const redactedValue = "[redacted]";

const secretKeys = new Set([
  "authorization",
  "cookie",
  "setcookie",
  "dsn",
  "databaseurl",
  "connectionstring",
  "privatekey",
  "passwd",
  "credential",
  "credentials",
  "csrf",
  "recoverycodes",
  "otpauthuri",
  "otpauthurl",
]);
const secretSuffixes = ["password", "token", "secret", "apikey", "key"];

/** Same rule as the server's body log (internal/adapter/http/devbody.go). */
export function isSecretKey(key: string): boolean {
  const normalized = key.toLowerCase().replace(/[_.-]/g, "");
  return secretKeys.has(normalized) || secretSuffixes.some((suffix) => normalized.endsWith(suffix));
}

/** Replaces the values of credential-like keys at any depth. */
export function redactJson(value: Json): Json {
  if (Array.isArray(value)) {
    return value.map(redactJson);
  }
  if (isObject(value)) {
    return Object.fromEntries(Object.entries(value).map(([key, inner]) => [key, isSecretKey(key) ? redactedValue : redactJson(inner)]));
  }
  return value;
}

function shellQuote(value: string): string {
  return "'" + value.replace(/'/g, "'\\''") + "'";
}

/**
 * Renders the request as a cURL command. The session cookie and the CSRF
 * token are placeholders, and credential-like body fields are redacted:
 * the clipboard never receives a real credential.
 */
export function curlCommand(origin: string, method: ConsoleMethod, request: BuiltRequest): string {
  const upper = method.toUpperCase();
  const lines = ["curl -sS -X " + upper + " " + shellQuote(origin + request.path + queryString(request.query))];
  lines.push("-H " + shellQuote("Cookie: " + sessionCookiePlaceholder));
  if (method !== "get" && method !== "head") {
    lines.push("-H " + shellQuote("X-Jelee-CSRF: " + csrfPlaceholder));
  }
  lines.push("-H " + shellQuote("Accept: application/json"));
  for (const [name, value] of Object.entries(request.headers)) {
    lines.push("-H " + shellQuote(name + ": " + value));
  }
  if (request.body !== undefined) {
    lines.push("-H " + shellQuote("Content-Type: application/json"));
    lines.push("--data-raw " + shellQuote(JSON.stringify(redactJson(request.body))));
  }
  return lines.join(" \\\n  ");
}

/** Response headers whose values the console never shows. */
const sensitiveHeaders = new Set(["set-cookie", "set-cookie2", "cookie", "authorization", "proxy-authorization", "x-jelee-csrf", "x-jelee-setup-token"]);

export function isSensitiveHeader(name: string): boolean {
  return sensitiveHeaders.has(name.toLowerCase());
}

/** Response headers sorted by name, credential headers masked. */
export function maskedHeaders(headers: Headers): [string, string][] {
  const entries: [string, string][] = [];
  headers.forEach((value, name) => {
    entries.push([name, isSensitiveHeader(name) ? redactedValue : value]);
  });
  return entries.sort(([a], [b]) => a.localeCompare(b));
}

/**
 * Formats a response body for display: indented JSON with credential-like
 * fields redacted, plain text as is, anything else by size only.
 */
export function formatBody(contentType: string, raw: string): { kind: "json" | "text" | "binary" | "empty"; text: string } {
  if (raw === "") {
    return { kind: "empty", text: "" };
  }
  const type = contentType.toLowerCase();
  if (type.includes("json")) {
    try {
      return { kind: "json", text: JSON.stringify(redactJson(JSON.parse(raw) as Json), null, 2) };
    } catch {
      return { kind: "text", text: raw };
    }
  }
  if (type.startsWith("text/") || type.includes("xml") || type === "") {
    return { kind: "text", text: raw };
  }
  return { kind: "binary", text: "" };
}
