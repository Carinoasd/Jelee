import { describe, expect, it } from "vitest";
import fixture from "./qr.fixture.json";
import { encodeQr, qrPath, QrCapacityError, type QrCode } from "./qr";

// qr.fixture.json holds symbols made by an independent reference encoder
// (segno 1.6.6: level M, byte mode of the UTF-8 bytes, no error level boost):
// version, chosen mask, dark module count and the CRC-32 of the module
// matrix written row by row as "0"/"1" characters. The generator corrected
// one deviation of segno 1.6.6 first: on a codeword boundary it appends a
// whole zero byte of padding bits, where ISO/IEC 18004 7.4.10 adds none.

interface Case {
  name: string;
  text?: string;
  sweep?: number[];
  forceMask?: number;
  version: number;
  mask: number;
  size: number;
  dark: number;
  crc32: number;
}

/** Printable ASCII filler, the same as the fixture generator's. */
function sweepText(version: number, length: number): string {
  return Array.from({ length }, (_, i) => String.fromCharCode(33 + ((i * 7 + version * 13) % 94))).join("");
}

function crc32(text: string): number {
  let crc = 0xffffffff;
  for (let i = 0; i < text.length; i++) {
    crc ^= text.charCodeAt(i);
    for (let k = 0; k < 8; k++) {
      crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function digest(code: QrCode) {
  const bits = code.modules.map((row) => row.map((dark) => (dark ? "1" : "0")).join("")).join("");
  return { version: code.version, mask: code.mask, size: code.size, dark: bits.replaceAll("0", "").length, crc32: crc32(bits) };
}

const cases = fixture.cases as Case[];

describe("QR encoder", () => {
  it.each(cases.filter((entry) => entry.sweep === undefined).map((entry) => [entry.name, entry] as const))(
    "matches the reference symbol for %s",
    (_name, entry) => {
      const code = encodeQr(entry.text ?? "", entry.forceMask === undefined ? {} : { mask: entry.forceMask });
      expect(digest(code)).toEqual({ version: entry.version, mask: entry.mask, size: entry.size, dark: entry.dark, crc32: entry.crc32 });
    },
  );

  it("matches the reference at both capacity edges of every version 1-40", () => {
    const sweep = cases.filter((entry) => entry.sweep !== undefined);
    expect(sweep.length).toBeGreaterThanOrEqual(79);
    for (const entry of sweep) {
      const [version = 0, length = 0] = entry.sweep ?? [];
      const code = encodeQr(sweepText(version, length));
      expect({ name: entry.name, ...digest(code) }).toEqual({
        name: entry.name,
        version: entry.version,
        mask: entry.mask,
        size: entry.size,
        dark: entry.dark,
        crc32: entry.crc32,
      });
    }
  });

  it("refuses data beyond version 40", () => {
    expect(() => encodeQr("x".repeat((fixture.capacity[39] ?? 0) + 1))).toThrow(QrCapacityError);
  });

  it("draws dark runs as SVG path data inside the quiet zone", () => {
    const code: QrCode = { version: 1, mask: 0, size: 3, modules: [[true, true, false], [false, false, false], [false, true, true]] };
    expect(qrPath(code)).toBe("M4 4h2v1h-2zM5 6h2v1h-2z");
    expect(qrPath(code, 0)).toBe("M0 0h2v1h-2zM1 2h2v1h-2z");
  });
});
