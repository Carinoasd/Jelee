/** One labelled value of a bar or column chart. */
export interface BarRow {
  readonly key: string;
  readonly label: string;
  readonly value: number;
  /** Formatted value shown next to the bar and read by screen readers. */
  readonly display: string;
}

/** One choice of UiSelectField. */
export interface SelectOption {
  readonly value: string;
  readonly label: string;
}
