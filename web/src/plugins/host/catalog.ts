// Discovery of the plugins bundled with the web client. Manifests are read
// eagerly (small JSON, validated before any plugin code runs); entries and
// components are separate chunks loaded only for enabled plugins (G32.3).
// There is no remote plugin loading: every plugin is built and reviewed with
// the application, which is what the front-end isolation relies on.

export interface PluginBundle {
  /** Raw manifest; validated by the host before anything else. */
  readonly manifest: unknown;
  /** Imports the module the manifest's entry names. */
  readonly load: (entry: string) => Promise<unknown>;
  /** Enabled until an administrator decides otherwise. */
  readonly enabledByDefault: boolean;
  readonly official: boolean;
}

/** Thrown when a manifest names an entry module that does not exist. */
export class PluginEntryMissing extends Error {
  constructor(entry: string) {
    super("plugin entry not found: " + entry);
    this.name = "PluginEntryMissing";
  }
}

const manifests = import.meta.glob<unknown>("../official/*/manifest.json", { eager: true, import: "default" });
const modules = import.meta.glob<unknown>(["../official/*/*.ts", "!../official/**/*.test.ts"]);

/** Official plugins enabled out of the box; the others are opt-in examples. */
const enabledOfficial = new Set(["jelee.item-facts"]);

export function officialPlugins(): PluginBundle[] {
  return Object.entries(manifests).map(([path, manifest]) => {
    const directory = path.slice(0, -"manifest.json".length);
    const id = typeof manifest === "object" && manifest !== null && "id" in manifest ? manifest.id : null;
    return {
      manifest,
      official: true,
      enabledByDefault: typeof id === "string" && enabledOfficial.has(id),
      load: (entry: string) => {
        const loader = modules[directory + entry.replace(/^\.\//, "")];
        return loader === undefined ? Promise.reject(new PluginEntryMissing(entry)) : loader();
      },
    };
  });
}
