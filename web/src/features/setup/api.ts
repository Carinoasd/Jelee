import type { ApiClient } from "@/api/client";
import { ApiError, networkError, toApiError } from "@/api/errors";
import type { components, paths } from "@/api/schema";

export type SetupState = components["schemas"]["SetupState"];
export type SetupStep = paths["/api/v1/setup/steps/{step}"]["post"]["parameters"]["path"]["step"];
type StepBody = NonNullable<paths["/api/v1/setup/steps/{step}"]["post"]["requestBody"]>["content"]["application/json"];

/** One fixed-code validation problem; the server never echoes input values. */
export interface SetupIssue {
  field: string;
  code: string;
}

/** The server rejected step input with setup_validation_failed. */
export class SetupInputError extends Error {
  readonly issues: readonly SetupIssue[];

  constructor(issues: readonly SetupIssue[]) {
    super("setup input needs correction");
    this.name = "SetupInputError";
    this.issues = issues;
  }
}

export const setupTokenHeader = "X-Jelee-Setup-Token";

function issuesOf(body: unknown): SetupIssue[] | null {
  if (typeof body !== "object" || body === null || !("error" in body)) {
    return null;
  }
  const inner: unknown = body.error;
  if (typeof inner !== "object" || inner === null || !("code" in inner) || inner.code !== "setup_validation_failed") {
    return null;
  }
  const details: unknown = "details" in inner ? inner.details : null;
  const list: unknown = typeof details === "object" && details !== null && "issues" in details ? details.issues : null;
  if (!Array.isArray(list)) {
    return [];
  }
  return list.filter(
    (entry: unknown): entry is SetupIssue =>
      typeof entry === "object" &&
      entry !== null &&
      "field" in entry &&
      typeof entry.field === "string" &&
      "code" in entry &&
      typeof entry.code === "string",
  );
}

interface WizardResult {
  data?: { data: SetupState };
  error?: unknown;
  response: Response;
}

async function wizard(pending: Promise<WizardResult>): Promise<SetupState> {
  let result: WizardResult;
  try {
    result = await pending;
  } catch (cause: unknown) {
    throw cause instanceof ApiError ? cause : networkError(cause);
  }
  if (result.response.ok && result.data !== undefined) {
    return result.data.data;
  }
  const issues = issuesOf(result.error);
  if (issues !== null) {
    throw new SetupInputError(issues);
  }
  throw toApiError(result.error, result.response);
}

/**
 * Asks whether this server still needs initial setup. Any answer other than
 * an explicit 200 (410 after setup, older servers, network failures) means
 * the normal application should load.
 */
export async function setupRequired(client: ApiClient): Promise<boolean> {
  try {
    const { response } = await client.GET("/api/v1/setup/status", {});
    return response.status === 200;
  } catch {
    return false;
  }
}

const header = (token: string) => ({ [setupTokenHeader]: token });

export function readSetup(client: ApiClient, token: string): Promise<SetupState> {
  return wizard(client.GET("/api/v1/setup", { params: { header: header(token) } }));
}

export function submitSetupStep(client: ApiClient, token: string, step: SetupStep, body?: StepBody): Promise<SetupState> {
  return wizard(
    client.POST("/api/v1/setup/steps/{step}", {
      params: { header: header(token), path: { step } },
      ...(body === undefined ? {} : { body }),
    }),
  );
}

export function setupBack(client: ApiClient, token: string): Promise<SetupState> {
  return wizard(client.POST("/api/v1/setup/back", { params: { header: header(token) } }));
}

export function completeSetup(client: ApiClient, token: string): Promise<SetupState> {
  return wizard(client.POST("/api/v1/setup/complete", { params: { header: header(token) } }));
}
