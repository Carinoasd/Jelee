// Window arithmetic of UiVirtualList (G35.3), kept free of the DOM so it is
// tested directly.

export interface VirtualInput {
  /** Items in the list. */
  readonly count: number;
  /** Items per row (1 for a plain list). */
  readonly columns: number;
  /** Distance from one row's top to the next, in pixels. */
  readonly rowHeight: number;
  /** Viewport top relative to the list's top (negative while the list is below it). */
  readonly viewportTop: number;
  readonly viewportHeight: number;
  /** Rows rendered beyond each edge of the viewport. */
  readonly overscan: number;
}

export interface VirtualWindow {
  /** First rendered item. */
  readonly start: number;
  /** One past the last rendered item. */
  readonly end: number;
  /** Space standing in for the rows above and below, in pixels. */
  readonly before: number;
  readonly after: number;
}

/** Rows to render for a scroll position; always whole rows. */
export function virtualWindow(input: VirtualInput): VirtualWindow {
  const columns = Math.max(1, Math.floor(input.columns));
  const rowHeight = Math.max(1, input.rowHeight);
  const rows = Math.ceil(Math.max(0, input.count) / columns);
  const overscan = Math.max(0, Math.floor(input.overscan));
  const firstRow = Math.min(rows, Math.max(0, Math.floor(input.viewportTop / rowHeight) - overscan));
  const lastRow = Math.min(rows, Math.max(firstRow, Math.ceil((input.viewportTop + input.viewportHeight) / rowHeight) + overscan));
  return {
    start: firstRow * columns,
    end: Math.min(input.count, lastRow * columns),
    before: firstRow * rowHeight,
    after: (rows - lastRow) * rowHeight,
  };
}

/**
 * Columns of a grid from its computed grid-template-columns (the browser
 * resolves auto-fill to one size per track); 1 without a grid.
 */
export function countTracks(tracks: string): number {
  const value = tracks.trim();
  if (value === "" || value === "none") {
    return 1;
  }
  // Split on spaces outside parentheses (minmax(), repeat()).
  let depth = 0;
  let count = 1;
  let previousSpace = false;
  for (const char of value) {
    if (char === "(") {
      depth++;
    } else if (char === ")") {
      depth--;
    }
    const space = depth === 0 && /\s/.test(char);
    if (space && !previousSpace) {
      count++;
    }
    previousSpace = space;
  }
  return count;
}
