// deepClone copies plain Kong entity data.
//
// `structuredClone` cannot be used here: entities live inside a Pinia store, so
// what reaches these helpers is a reactive Proxy, and cloning a Proxy throws
// DataCloneError — which silently dropped plugin config edits before they were
// ever sent to Kong. Kong entities are JSON all the way down, so a JSON
// round-trip is both safe and proxy-agnostic.
export function deepClone(value) {
  if (value === undefined || value === null) return value
  return JSON.parse(JSON.stringify(value))
}
