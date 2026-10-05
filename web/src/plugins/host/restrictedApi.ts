// The data access a plugin receives (G32.3). It is a frozen object of
// closures over the host's API client: the client, the credential strategy
// and the CSRF value are never properties of anything a plugin can reach.
// Only fixed GET endpoints exist, each behind a manifest permission; results
// are plain copies mapped to SDK types, and errors carry only the server's
// error code. Writes are not offered at all.
//
// Front-end limits (docs/frontend-adr.md): plugins run in the page's realm,
// so this is a capability boundary for reviewed, bundled plugin code, backed
// by ESLint rules for plugin sources and by the server's own authorization.
// It cannot stop deliberately hostile code in the same origin.
import { PluginApiError, PluginPermissionError, type PluginApi, type PluginItem, type PluginPermission, type PluginSourceSummary } from "@jelee/plugin-sdk";
import { ApiError } from "@/api/errors";
import type { ApiClient } from "@/api/client";
import { currentUser } from "@/features/auth/api";
import { getItemDetails, getItemSources, type ItemDetails, type MediaSourceInfo } from "@/features/items/api";
import { listLibraries } from "@/features/libraries/api";

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Maps catalog details to the plugin-visible item. */
export function toPluginItem(details: ItemDetails): PluginItem {
  return Object.freeze({
    id: details.id,
    libraryId: details.libraryId,
    kind: details.kind,
    title: details.title,
    originalTitle: details.originalTitle ?? null,
    productionYear: details.productionYear ?? null,
    hasOverview: (details.overview ?? "") !== "",
    genres: Object.freeze([...details.genres]),
    externalIds: Object.freeze(details.externalIds.map((id) => Object.freeze({ type: id.type, value: id.value }))),
    nfoStatus: details.nfo.status,
  });
}

function toSourceSummary(source: MediaSourceInfo): PluginSourceSummary {
  const video = source.videoTracks[0];
  return Object.freeze({
    container: source.container,
    sizeBytes: source.sizeBytes ?? null,
    durationSeconds: source.durationMicros === undefined ? null : Math.round(source.durationMicros / 1_000_000),
    width: video?.width ?? null,
    height: video?.height ?? null,
  });
}

async function guarded<T>(operation: () => Promise<T>): Promise<T> {
  try {
    return await operation();
  } catch (error: unknown) {
    if (error instanceof ApiError) {
      throw new PluginApiError(error.code, error.status);
    }
    throw new PluginApiError("unexpected_response", 0);
  }
}

export function createPluginApi(client: ApiClient, permissions: readonly PluginPermission[]): PluginApi {
  const granted = new Set(permissions);
  const need = (permission: PluginPermission) => {
    if (!granted.has(permission)) {
      throw new PluginPermissionError(permission);
    }
  };
  const itemId = (id: string) => {
    if (!uuid.test(id)) {
      throw new PluginApiError("invalid_request", 400);
    }
    return id;
  };
  const api: PluginApi = {
    async item(id) {
      need("catalog.read");
      const checked = itemId(id);
      return guarded(async () => toPluginItem(await getItemDetails(client, checked)));
    },
    async itemSources(id) {
      need("catalog.read");
      const checked = itemId(id);
      return guarded(async () => Object.freeze((await getItemSources(client, checked)).map(toSourceSummary)));
    },
    async libraries() {
      need("catalog.read");
      return guarded(async () => Object.freeze((await listLibraries(client)).libraries.map((library) => Object.freeze({ id: library.id, name: library.name }))));
    },
    async me() {
      need("user.read");
      return guarded(async () => {
        const user = await currentUser(client);
        return Object.freeze({ name: user.name, displayName: user.displayName, locale: user.locale, admin: user.admin });
      });
    },
  };
  return Object.freeze(api);
}
