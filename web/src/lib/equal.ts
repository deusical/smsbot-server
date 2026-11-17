/** Fast structural equality for plain JSON data, short-circuiting on the first difference. */
export function deepEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || a === null || typeof b !== "object" || b === null) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) {
      if (!deepEqual(a[i], b[i])) return false;
    }
    return true;
  }
  if (Array.isArray(b)) return false;
  const objA = a as Record<string, unknown>;
  const objB = b as Record<string, unknown>;
  const keysA = Object.keys(objA);
  const keysB = Object.keys(objB);
  let definedA = 0;
  for (let i = 0; i < keysA.length; i++) {
    const key = keysA[i];
    const valA = objA[key];
    if (valA === undefined) continue;
    definedA++;
    if (!Object.prototype.hasOwnProperty.call(objB, key) || !deepEqual(valA, objB[key])) {
      return false;
    }
  }
  let definedB = 0;
  for (let i = 0; i < keysB.length; i++) {
    if (objB[keysB[i]] !== undefined) definedB++;
  }
  return definedA === definedB;
}
