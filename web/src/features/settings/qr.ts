// QR Code encoder (ISO/IEC 18004:2015) for the two-factor enrollment URI,
// without dependencies: byte mode, error correction level M, the smallest
// version 1-40 that holds the data, and the data mask with the lowest penalty
// score (evaluated, as 7.8 describes, before format and version information
// are added). qr.test.ts compares the output module by module with a
// reference encoder.

/** A finished symbol; modules[row][column] is true for a dark module. */
export interface QrCode {
  readonly version: number;
  readonly mask: number;
  readonly size: number;
  readonly modules: readonly (readonly boolean[])[];
}

// Error correction codewords per block and number of blocks at level M,
// indexed by version (index 0 unused). ISO/IEC 18004:2015 Table 9.
const eccPerBlock = [
  0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28,
  28, 28, 28, 28, 28, 28, 28, 28, 28, 28,
];
const blockCount = [
  0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37,
  38, 40, 43, 45, 47, 49,
];

/** Format information bits of level M (indicator 00). */
const levelMBits = 0;

export class QrCapacityError extends Error {
  constructor() {
    super("data too long for a QR code at level M");
    this.name = "QrCapacityError";
  }
}

/** Modules left for codewords after all function patterns of a version. */
function rawDataModules(version: number): number {
  let result = (16 * version + 128) * version + 64;
  if (version >= 2) {
    const align = Math.floor(version / 7) + 2;
    result -= (25 * align - 10) * align - 55;
    if (version >= 7) {
      result -= 36;
    }
  }
  return result;
}

function dataCodewords(version: number): number {
  return Math.floor(rawDataModules(version) / 8) - (eccPerBlock[version] ?? 0) * (blockCount[version] ?? 0);
}

/** Center coordinates of the alignment patterns (Annex E). */
function alignmentPositions(version: number): number[] {
  if (version === 1) {
    return [];
  }
  const count = Math.floor(version / 7) + 2;
  const step = Math.floor((version * 8 + count * 3 + 5) / (count * 4 - 4)) * 2;
  const result = [6];
  for (let position = version * 4 + 10; result.length < count; position -= step) {
    result.splice(1, 0, position);
  }
  return result;
}

// Reed-Solomon over GF(2^8) with the polynomial x^8 + x^4 + x^3 + x^2 + 1.
function gfMultiply(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z;
}

function rsDivisor(degree: number): number[] {
  const result = new Array<number>(degree).fill(0);
  result[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      result[j] = gfMultiply(result[j] ?? 0, root);
      if (j + 1 < degree) {
        result[j] = (result[j] ?? 0) ^ (result[j + 1] ?? 0);
      }
    }
    root = gfMultiply(root, 0x02);
  }
  return result;
}

function rsRemainder(data: readonly number[], divisor: readonly number[]): number[] {
  const result = new Array<number>(divisor.length).fill(0);
  for (const byte of data) {
    const factor = byte ^ (result.shift() ?? 0);
    result.push(0);
    divisor.forEach((coefficient, i) => {
      result[i] = (result[i] ?? 0) ^ gfMultiply(coefficient, factor);
    });
  }
  return result;
}

/** Bit stream of one byte-mode segment, terminated and padded (7.4). */
function encodeData(bytes: Uint8Array, version: number): number[] {
  const capacityBits = dataCodewords(version) * 8;
  const bits: number[] = [];
  const append = (value: number, length: number) => {
    for (let i = length - 1; i >= 0; i--) {
      bits.push((value >>> i) & 1);
    }
  };
  append(0b0100, 4);
  append(bytes.length, version <= 9 ? 8 : 16);
  for (const byte of bytes) {
    append(byte, 8);
  }
  append(0, Math.min(4, capacityBits - bits.length));
  append(0, (8 - (bits.length % 8)) % 8);
  for (let pad = 0xec; bits.length < capacityBits; pad ^= 0xec ^ 0x11) {
    append(pad, 8);
  }
  const codewords: number[] = [];
  for (let i = 0; i < bits.length; i += 8) {
    codewords.push(bits.slice(i, i + 8).reduce((byte, bit) => (byte << 1) | bit, 0));
  }
  return codewords;
}

/** Splits into blocks, appends error correction and interleaves (7.6). */
function finalMessage(data: readonly number[], version: number): number[] {
  const blocks = blockCount[version] ?? 1;
  const eccLength = eccPerBlock[version] ?? 0;
  const raw = Math.floor(rawDataModules(version) / 8);
  const shortBlocks = blocks - (raw % blocks);
  const shortLength = Math.floor(raw / blocks) - eccLength;
  const divisor = rsDivisor(eccLength);
  const dataBlocks: number[][] = [];
  const eccBlocks: number[][] = [];
  for (let i = 0, offset = 0; i < blocks; i++) {
    const length = shortLength + (i < shortBlocks ? 0 : 1);
    const block = data.slice(offset, offset + length);
    offset += length;
    dataBlocks.push(block);
    eccBlocks.push(rsRemainder(block, divisor));
  }
  const result: number[] = [];
  for (let i = 0; i <= shortLength; i++) {
    for (const block of dataBlocks) {
      if (i < block.length) {
        result.push(block[i] ?? 0);
      }
    }
  }
  for (let i = 0; i < eccLength; i++) {
    for (const block of eccBlocks) {
      result.push(block[i] ?? 0);
    }
  }
  return result;
}

const maskConditions: readonly ((row: number, column: number) => boolean)[] = [
  (i, j) => (i + j) % 2 === 0,
  (i) => i % 2 === 0,
  (_i, j) => j % 3 === 0,
  (i, j) => (i + j) % 3 === 0,
  (i, j) => (Math.floor(i / 2) + Math.floor(j / 3)) % 2 === 0,
  (i, j) => ((i * j) % 2) + ((i * j) % 3) === 0,
  (i, j) => (((i * j) % 2) + ((i * j) % 3)) % 2 === 0,
  (i, j) => (((i + j) % 2) + ((i * j) % 3)) % 2 === 0,
];

class Matrix {
  readonly modules: Uint8Array[];
  /** Function pattern and reserved (format, version) modules. */
  readonly reserved: Uint8Array[];

  readonly size: number;

  constructor(size: number) {
    this.size = size;
    this.modules = Array.from({ length: size }, () => new Uint8Array(size));
    this.reserved = Array.from({ length: size }, () => new Uint8Array(size));
  }

  set(row: number, column: number, dark: boolean): void {
    (this.modules[row] as Uint8Array)[column] = dark ? 1 : 0;
    (this.reserved[row] as Uint8Array)[column] = 1;
  }

  isReserved(row: number, column: number): boolean {
    return (this.reserved[row] as Uint8Array)[column] === 1;
  }
}

function drawFunctionPatterns(matrix: Matrix, version: number): void {
  const size = matrix.size;
  for (let i = 0; i < size; i++) {
    matrix.set(6, i, i % 2 === 0);
    matrix.set(i, 6, i % 2 === 0);
  }
  // Finder patterns with their separators.
  for (const [top, left] of [
    [3, 3],
    [3, size - 4],
    [size - 4, 3],
  ] as const) {
    for (let dy = -4; dy <= 4; dy++) {
      for (let dx = -4; dx <= 4; dx++) {
        const row = top + dy;
        const column = left + dx;
        if (row >= 0 && row < size && column >= 0 && column < size) {
          const distance = Math.max(Math.abs(dx), Math.abs(dy));
          matrix.set(row, column, distance !== 2 && distance !== 4);
        }
      }
    }
  }
  const positions = alignmentPositions(version);
  const last = positions.length - 1;
  positions.forEach((row, i) => {
    positions.forEach((column, j) => {
      // The three corners occupied by finder patterns are skipped.
      if ((i === 0 && j === 0) || (i === 0 && j === last) || (i === last && j === 0)) {
        return;
      }
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          matrix.set(row + dy, column + dx, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
        }
      }
    });
  });
  // Format and version areas stay light until the mask is chosen; row and
  // column 6 there belong to the timing patterns.
  for (let i = 0; i < 9; i++) {
    if (i !== 6) {
      matrix.set(8, i, false);
      matrix.set(i, 8, false);
    }
  }
  for (let i = 0; i < 8; i++) {
    matrix.set(8, size - 1 - i, false);
    matrix.set(size - 1 - i, 8, false);
  }
  if (version >= 7) {
    for (let i = 0; i < 18; i++) {
      matrix.set(size - 11 + (i % 3), Math.floor(i / 3), false);
      matrix.set(Math.floor(i / 3), size - 11 + (i % 3), false);
    }
  }
}

/** Places the codewords in the zigzag order of 7.7.3. */
function drawCodewords(matrix: Matrix, codewords: readonly number[]): void {
  const size = matrix.size;
  const total = codewords.length * 8;
  let bit = 0;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) {
      right = 5;
    }
    const upward = ((right + 1) & 2) === 0;
    for (let vertical = 0; vertical < size; vertical++) {
      const row = upward ? size - 1 - vertical : vertical;
      for (let k = 0; k < 2; k++) {
        const column = right - k;
        if (!matrix.isReserved(row, column)) {
          const dark = bit < total && (((codewords[bit >>> 3] ?? 0) >>> (7 - (bit & 7))) & 1) === 1;
          (matrix.modules[row] as Uint8Array)[column] = dark ? 1 : 0;
          bit++;
        }
      }
    }
  }
}

function applyMask(matrix: Matrix, mask: number): Uint8Array[] {
  const condition = maskConditions[mask] as (row: number, column: number) => boolean;
  return matrix.modules.map((source, row) =>
    source.map((value, column) => (!matrix.isReserved(row, column) && condition(row, column) ? value ^ 1 : value)),
  );
}

const finderLike = [1, 0, 1, 1, 1, 0, 1];

/** Penalty points N3 of one row or column (1:1:3:1:1 with 4 light modules on a side). */
function finderPenalty(line: Uint8Array): number {
  const size = line.length;
  const find = (from: number) => {
    for (let i = from; i + 7 <= size; i++) {
      if (finderLike.every((value, k) => line[i + k] === value)) {
        return i;
      }
    }
    return -1;
  };
  const allLight = (from: number, to: number) => {
    for (let i = Math.max(from, 0); i < Math.min(to, size); i++) {
      if (line[i] === 1) {
        return false;
      }
    }
    return true;
  };
  let score = 0;
  let index = find(0);
  while (index !== -1) {
    let next = index + 7;
    if (index === 0 || index === size - 7 || allLight(index - 4, index) || allLight(index + 7, index + 11)) {
      score += 40;
    } else {
      next = index + 4;
    }
    index = find(next);
  }
  return score;
}

/** Penalty score of a masked symbol (7.8.3, Table 11). */
export function penaltyScore(modules: readonly Uint8Array[]): number {
  const size = modules.length;
  const at = (row: number, column: number) => (modules[row] as Uint8Array)[column] ?? 0;
  let score = 0;
  let dark = 0;
  const column = new Uint8Array(size);
  for (let i = 0; i < size; i++) {
    let rowRun = 0;
    let columnRun = 0;
    for (let j = 0; j < size; j++) {
      const value = at(i, j);
      dark += value;
      column[j] = at(j, i);
      if (j > 0 && value === at(i, j - 1)) {
        rowRun++;
      } else {
        score += rowRun >= 5 ? rowRun - 2 : 0;
        rowRun = 1;
      }
      if (j > 0 && at(j, i) === at(j - 1, i)) {
        columnRun++;
      } else {
        score += columnRun >= 5 ? columnRun - 2 : 0;
        columnRun = 1;
      }
      if (i > 0 && j > 0 && value === at(i, j - 1) && value === at(i - 1, j) && value === at(i - 1, j - 1)) {
        score += 3;
      }
    }
    score += rowRun >= 5 ? rowRun - 2 : 0;
    score += columnRun >= 5 ? columnRun - 2 : 0;
    score += finderPenalty(modules[i] as Uint8Array) + finderPenalty(column);
  }
  return score + 10 * Math.floor(Math.abs((dark / (size * size)) * 100 - 50) / 5);
}

function drawFormatBits(modules: Uint8Array[], mask: number): void {
  const size = modules.length;
  const data = (levelMBits << 3) | mask;
  let remainder = data;
  for (let i = 0; i < 10; i++) {
    remainder = (remainder << 1) ^ ((remainder >>> 9) * 0x537);
  }
  const bits = ((data << 10) | remainder) ^ 0x5412;
  const put = (row: number, column: number, index: number) => {
    (modules[row] as Uint8Array)[column] = (bits >>> index) & 1;
  };
  for (let i = 0; i <= 5; i++) {
    put(i, 8, i);
  }
  put(7, 8, 6);
  put(8, 8, 7);
  put(8, 7, 8);
  for (let i = 9; i < 15; i++) {
    put(8, 14 - i, i);
  }
  for (let i = 0; i < 8; i++) {
    put(8, size - 1 - i, i);
  }
  for (let i = 8; i < 15; i++) {
    put(size - 15 + i, 8, i);
  }
  (modules[size - 8] as Uint8Array)[8] = 1;
}

function drawVersionBits(modules: Uint8Array[], version: number): void {
  if (version < 7) {
    return;
  }
  const size = modules.length;
  let remainder = version;
  for (let i = 0; i < 12; i++) {
    remainder = (remainder << 1) ^ ((remainder >>> 11) * 0x1f25);
  }
  const bits = (version << 12) | remainder;
  for (let i = 0; i < 18; i++) {
    const bit = (bits >>> i) & 1;
    const a = size - 11 + (i % 3);
    const b = Math.floor(i / 3);
    (modules[b] as Uint8Array)[a] = bit;
    (modules[a] as Uint8Array)[b] = bit;
  }
}

/**
 * Encodes text (as UTF-8) at level M. A mask from 0 to 7 may be forced;
 * otherwise the one with the lowest penalty wins (the first on a tie).
 */
export function encodeQr(text: string, options: { mask?: number } = {}): QrCode {
  const bytes = new TextEncoder().encode(text);
  let version = 1;
  for (; ; version++) {
    if (version > 40) {
      throw new QrCapacityError();
    }
    const header = 4 + (version <= 9 ? 8 : 16);
    if (header + bytes.length * 8 <= dataCodewords(version) * 8) {
      break;
    }
  }
  const matrix = new Matrix(version * 4 + 17);
  drawFunctionPatterns(matrix, version);
  drawCodewords(matrix, finalMessage(encodeData(bytes, version), version));
  let mask = options.mask ?? -1;
  let best: Uint8Array[] | null = null;
  if (mask >= 0) {
    best = applyMask(matrix, mask);
  } else {
    let bestScore = Infinity;
    for (let candidate = 0; candidate < 8; candidate++) {
      const masked = applyMask(matrix, candidate);
      const score = penaltyScore(masked);
      if (score < bestScore) {
        bestScore = score;
        best = masked;
        mask = candidate;
      }
    }
  }
  const modules = best ?? applyMask(matrix, 0);
  drawFormatBits(modules, mask);
  drawVersionBits(modules, version);
  return { version, mask, size: matrix.size, modules: modules.map((row) => Array.from(row, (value) => value === 1)) };
}

/**
 * SVG path data drawing the dark modules as horizontal runs, offset by the
 * quiet zone (in modules).
 */
export function qrPath(code: QrCode, quietZone = 4): string {
  const parts: string[] = [];
  code.modules.forEach((row, y) => {
    let x = 0;
    while (x < row.length) {
      if (!row[x]) {
        x++;
        continue;
      }
      const start = x;
      while (x < row.length && row[x]) {
        x++;
      }
      parts.push(`M${start + quietZone} ${y + quietZone}h${x - start}v1h${start - x}z`);
    }
  });
  return parts.join("");
}
