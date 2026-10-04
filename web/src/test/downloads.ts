// Captures file downloads started through api/download.ts: jsdom neither
// implements object URLs nor navigates on an anchor click.
import { vi } from "vitest";

export interface CapturedDownload {
  readonly fileName: string;
  readonly blob: Blob;
}

export function captureDownloads(): { readonly downloads: CapturedDownload[]; readonly revoked: string[]; restore(): void } {
  const downloads: CapturedDownload[] = [];
  const revoked: string[] = [];
  const blobs = new Map<string, Blob>();
  const create = vi.fn((blob: Blob) => {
    const url = `blob:test/${blobs.size + 1}`;
    blobs.set(url, blob);
    return url;
  });
  const revoke = vi.fn((url: string) => {
    revoked.push(url);
  });
  // jsdom may lack these statics, so the descriptors are swapped, not spied.
  const originals = {
    createObjectURL: Object.getOwnPropertyDescriptor(URL, "createObjectURL"),
    revokeObjectURL: Object.getOwnPropertyDescriptor(URL, "revokeObjectURL"),
  };
  Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: create });
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: revoke });
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    const blob = blobs.get(this.href);
    if (blob !== undefined) {
      downloads.push({ fileName: this.download, blob });
    }
  });
  return {
    downloads,
    revoked,
    restore() {
      for (const [name, descriptor] of Object.entries(originals)) {
        if (descriptor === undefined) {
          Reflect.deleteProperty(URL, name);
        } else {
          Object.defineProperty(URL, name, descriptor);
        }
      }
      click.mockRestore();
    },
  };
}
