// Data rights of the signed-in account (G07.7): download of the personal
// data and permanent deletion. The server checks every rule again.
import type { ApiClient } from "@/api/client";
import { callNoContent } from "@/api/call";
import { downloadAttachment } from "@/api/download";
import type { SecondFactorProof } from "@/features/auth/secondFactor";

/** File name of an export made today, e.g. jelee-my-data-2026-10-06.ndjson. */
export function exportFileName(today: Date = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `jelee-my-data-${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}.ndjson`;
}

/**
 * Downloads every record the server keeps about the user as NDJSON. The
 * server audits the export; secrets are never part of it. A busy server
 * (another export running) is thrown as an ApiError conflict.
 */
export async function downloadMyData(client: ApiClient, userId: string): Promise<void> {
  await downloadAttachment(
    client.GET("/api/v1/users/{id}/data-export", { params: { path: { id: userId } }, parseAs: "blob" }),
    exportFileName(),
  );
}

/**
 * Deletes the account and all its data for good. Needs the password and,
 * with two-step verification on, a code or a recovery code. The session
 * ends with the account.
 */
export async function deleteMyAccount(client: ApiClient, password: string, proof: SecondFactorProof | null): Promise<void> {
  await callNoContent(client.POST("/api/v1/users/me/purge", { body: { password, ...proof } }));
}
