// Pure helpers of the library grant matrix (G48.7): the edited grants are
// compared with the server's and turned into the operations of one bulk
// change. The server validates everything again.
import type { AccessGrantMatrix, GrantOperation } from "./api";

/** Granted library IDs by user ID. */
export type Grants = ReadonlyMap<string, ReadonlySet<string>>;

export function grantsOf(matrix: AccessGrantMatrix): Grants {
  return new Map(matrix.users.map((user) => [user.id, new Set(user.libraryIds)]));
}

/** What one user gains and loses, library IDs in matrix column order. */
export interface GrantDiff {
  readonly userId: string;
  readonly added: readonly string[];
  readonly removed: readonly string[];
}

/**
 * Users whose edited grants differ from the server's, in matrix row order.
 * libraryOrder fixes the order of the IDs; IDs outside it come last, sorted.
 */
export function grantDiffs(server: Grants, draft: Grants, libraryOrder: readonly string[]): GrantDiff[] {
  const rank = new Map(libraryOrder.map((id, index) => [id, index]));
  const ordered = (ids: Iterable<string>) =>
    [...ids].sort((a, b) => (rank.get(a) ?? Infinity) - (rank.get(b) ?? Infinity) || (a < b ? -1 : a > b ? 1 : 0));
  const diffs: GrantDiff[] = [];
  for (const [userId, next] of draft) {
    const before = server.get(userId) ?? new Set<string>();
    const added = ordered([...next].filter((id) => !before.has(id)));
    const removed = ordered([...before].filter((id) => !next.has(id)));
    if (added.length > 0 || removed.length > 0) {
      diffs.push({ userId, added, removed });
    }
  }
  return diffs;
}

/**
 * Bulk operations for the differences: users gaining the same set of
 * libraries share one add operation and users losing the same set share one
 * remove operation, so at most two operations per changed user are sent.
 */
export function grantOperations(diffs: readonly GrantDiff[]): GrantOperation[] {
  const groups = new Map<string, GrantOperation>();
  function put(action: GrantOperation["action"], userId: string, libraryIds: readonly string[]) {
    if (libraryIds.length === 0) {
      return;
    }
    const key = action + "\n" + libraryIds.join("\n");
    const group = groups.get(key);
    if (group === undefined) {
      groups.set(key, { action, userIds: [userId], libraryIds: [...libraryIds] });
    } else {
      group.userIds.push(userId);
    }
  }
  for (const diff of diffs) {
    put("add", diff.userId, diff.added);
  }
  for (const diff of diffs) {
    put("remove", diff.userId, diff.removed);
  }
  return [...groups.values()];
}

/** Whether the user's edited grant of a library differs from the server's. */
export function cellChanged(server: Grants, draft: Grants, userId: string, libraryId: string): boolean {
  return (server.get(userId)?.has(libraryId) ?? false) !== (draft.get(userId)?.has(libraryId) ?? false);
}

/** Grants with one cell set to on. */
export function withCell(draft: Grants, userId: string, libraryId: string, on: boolean): Grants {
  return withCells(draft, [userId], [libraryId], on);
}

/** Grants with every listed user × library cell set to on. */
export function withCells(draft: Grants, userIds: readonly string[], libraryIds: readonly string[], on: boolean): Grants {
  const next = new Map(draft);
  for (const userId of userIds) {
    const set = new Set(next.get(userId) ?? []);
    for (const libraryId of libraryIds) {
      if (on) {
        set.add(libraryId);
      } else {
        set.delete(libraryId);
      }
    }
    next.set(userId, set);
  }
  return next;
}

/** Whether every listed cell is on (false for an empty list). */
export function allOn(draft: Grants, userIds: readonly string[], libraryIds: readonly string[]): boolean {
  return userIds.length > 0 && libraryIds.length > 0 && userIds.every((userId) => libraryIds.every((libraryId) => draft.get(userId)?.has(libraryId) ?? false));
}
