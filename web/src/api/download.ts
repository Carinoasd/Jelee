import { ApiError, networkError, toApiError } from "./errors";

/**
 * Downloads an attachment through the API client instead of a plain
 * <a download> link: the browser would save an error answer (401, 409 …) as
 * the file. Here an error is thrown as a normalized ApiError, so the caller
 * shows a localized message, and an expired session goes through the client's
 * usual 401 handling. The body is buffered in memory before it is saved.
 */
export async function downloadAttachment(
  pending: Promise<{ data?: Blob; error?: unknown; response: Response }>,
  fileName: string,
): Promise<void> {
  let result: { data?: Blob; error?: unknown; response: Response };
  try {
    result = await pending;
  } catch (cause: unknown) {
    throw cause instanceof ApiError ? cause : networkError(cause);
  }
  if (!result.response.ok || result.error !== undefined || result.data === undefined) {
    throw toApiError(result.error, result.response);
  }
  saveBlob(result.data, fileName);
}

/** Hands a Blob to the browser's download through a temporary object URL. */
export function saveBlob(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  anchor.hidden = true;
  document.body.append(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    // The click has started the download; the URL is no longer needed.
    setTimeout(() => {
      URL.revokeObjectURL(url);
    }, 0);
  }
}
