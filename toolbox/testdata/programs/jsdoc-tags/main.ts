/** Options for the operation */
interface Options {
  /** Only include items after this date */
  from: string;
  /** Only include items before this date */
  to: string;
}

/**
 * Perform a calculation with metadata.
 * @accessMode readOnly
 * @idempotent
 * @param a - The first number
 * @param b - The second number
 * @param options - Filtering options
 */
export default function(a: number, b: number, options: Options): number {
  return a + b;
}
