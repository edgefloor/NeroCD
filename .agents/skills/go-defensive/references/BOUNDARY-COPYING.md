# Copying Slices and Maps at API Boundaries

> **Source**: Uber Style Guide

Slices and maps contain references to their underlying data. Establish who may
mutate that data and how long each reference remains valid. Copy retained inputs
and exposed internal state unless an explicit safe ownership or immutability
contract permits sharing. Copying isolates mutations; it does not validate the
input's contents.

## Receiving Slices and Maps

When a function stores a slice or map passed by the caller, the caller can
modify the same data after the function returns. Make a defensive copy unless
the documented ownership contract makes that sharing safe. Check nested mutable
references as well as the outer collection.

### Slices

**Bad**
```go
func (d *Driver) SetTrips(trips []Trip) {
  d.trips = trips  // caller can still modify d.trips
}
```

**Good**
```go
func (d *Driver) SetTrips(trips []Trip) {
  d.trips = make([]Trip, len(trips))
  copy(d.trips, trips)
}
```

### Maps

**Bad**
```go
func (s *Server) SetConfig(cfg map[string]string) {
  s.config = cfg  // caller can still modify s.config
}
```

**Good**
```go
func (s *Server) SetConfig(cfg map[string]string) {
  s.config = make(map[string]string, len(cfg))
  for k, v := range cfg {
    s.config[k] = v
  }
}
```

## Returning Slices and Maps

When returning internal slices or maps, return a copy to prevent callers from
modifying internal state unless an explicit contract permits that shared state.
Check whether nested references still expose mutable state after the copy.

### Returning a Map

**Bad**
```go
func (s *Stats) Snapshot() map[string]int {
  s.mu.Lock()
  defer s.mu.Unlock()
  return s.counters  // exposes internal state!
}
```

**Good**
```go
func (s *Stats) Snapshot() map[string]int {
  s.mu.Lock()
  defer s.mu.Unlock()
  result := make(map[string]int, len(s.counters))
  for k, v := range s.counters {
    result[k] = v
  }
  return result
}
```

### Returning a Slice

**Bad**
```go
func (q *Queue) Items() []Item {
  return q.items  // caller can append, modify, or reslice
}
```

**Good**
```go
func (q *Queue) Items() []Item {
  result := make([]Item, len(q.items))
  copy(result, q.items)
  return result
}
```

## Inspect nested aliases

The examples copy only the outer slice or map. Assignment, `copy`, `slices.Clone`,
and `maps.Clone` leave nested references shared. If `Trip` or `Item` contains a
slice, map, or pointer to mutable data, isolate that data too or establish an
explicit safe sharing contract. The map examples have scalar values, so an
outer copy isolates their values.

Preserve the API's nil and empty collection semantics while copying. Ownership
changes must not accidentally change a JSON response from `[]` to `null`.

## When copies are not needed

Omitting a copy requires an explicit safe ownership or immutability contract.
For example:

- A synchronous borrow retains no references, and the contract excludes concurrent mutation during the call.
- An ownership transfer requires the sender to stop using all mutable aliases.
- A fresh result belongs only to the caller and has no mutable aliases retained internally, including nested references.
- Shared immutable data stays immutable through every alias for the full sharing lifetime.

Document which contract applies and verify that the implementation and callers
honor it. Profiling may show that copying is expensive, but that measurement
alone never proves that sharing is safe. Optimize within the ownership contract
or change the contract explicitly before removing a needed copy.

## Test ownership changes

When ownership behavior changes, add a focused mutation-isolation test for the
retained input or exposed output. Mutate the caller's original input after the
call and assert that retained state remains unchanged. Mutate a returned value
and assert that internal state remains unchanged. Include nested mutable fields
when present so a shallow copy cannot make the test pass accidentally.

If the API deliberately transfers ownership or shares immutable data, test the
observable contract it promises instead of assuming a defensive copy exists.
