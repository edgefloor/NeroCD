// Editors keep an identity when a row is replaced by an edit. Removing another
// row must not remount an input or attach its pending work to its new neighbor.
export function draftRowIdentity() {
  const keys = new WeakMap<object, string>();
  function key(row: object): string {
    let value = keys.get(row);
    if (value === undefined) {
      value = crypto.randomUUID();
      keys.set(row, value);
    }
    return value;
  }
  function replace<T extends object>(previous: T, next: T): T {
    keys.set(next, key(previous));
    return next;
  }
  return { key, replace };
}
