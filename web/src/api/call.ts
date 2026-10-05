import { ApiError, networkError, toApiError } from "./errors";

interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/**
 * Resolves an openapi-fetch call to its data or throws a normalized ApiError.
 * Network failures (fetch rejection) become "network_error".
 */
export async function call<T>(pending: Promise<FetchResult<T>>): Promise<T> {
  let result: FetchResult<T>;
  try {
    result = await pending;
  } catch (cause: unknown) {
    throw cause instanceof ApiError ? cause : networkError(cause);
  }
  if (!result.response.ok || result.error !== undefined) {
    throw toApiError(result.error, result.response);
  }
  if (result.data === undefined) {
    throw toApiError(undefined, result.response);
  }
  return result.data;
}

/** Like call() for endpoints that answer 204 without a body. */
export async function callNoContent(pending: Promise<{ error?: unknown; response: Response }>): Promise<void> {
  let result: { error?: unknown; response: Response };
  try {
    result = await pending;
  } catch (cause: unknown) {
    throw cause instanceof ApiError ? cause : networkError(cause);
  }
  if (!result.response.ok || result.error !== undefined) {
    throw toApiError(result.error, result.response);
  }
}
