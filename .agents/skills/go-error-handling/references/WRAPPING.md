# Error Wrapping Reference

This reference covers error wrapping with `%v` vs `%w`, placement conventions,
adding context to errors, and logging best practices.

---

## Wrapping Errors: %v vs %w

> **Advisory**: Recommended best practice.

Choose the verb from the caller's error contract. `%w` preserves the underlying
cause for `errors.Is` and `errors.As`; `%v` does not support unwrapping. Both
include the underlying error's text, so neither verb redacts sensitive details.

### Use %v for Simple Annotation

Use `%v` when you want to:

- Add context without preserving the error chain for programmatic inspection
- Deliberately exclude underlying-cause matching from the returned error contract
- Log or display errors to humans

```go
// The contract deliberately excludes matching the backend error.
if err != nil {
	return fmt.Errorf("query fortune database: %v", err)
}
```

### Use %w for Error Chain Preservation

Use `%w` when you want callers to programmatically inspect the underlying error:

```go
// Good: %w preserves error chain for errors.Is/errors.As
func (s *Server) internalFunction(ctx context.Context) error {
    if err != nil {
        return fmt.Errorf("couldn't find remote file: %w", err)
    }
}

// Caller can now check:
if errors.Is(err, fs.ErrNotExist) {
    // Handle not found case
}
```

### When to Use Each

**Use %w when**:
- Adding context while preserving the original error for programmatic inspection
- Preserving causes that callers already rely on for matching

**Use %v when**:
- Adding context when the contract deliberately excludes underlying-cause matching
- Logging or displaying to humans
- Creating an independent error without unwrapping the original cause

RPC, IPC, and storage boundaries do not determine the verb by themselves.
Preserve required matching causes inside the program. Separately translate the
error to a canonical external status and safe response message. For example,
an internal error may preserve `fs.ErrNotExist` with `%w` while the response
says only `resource not found`. Substituting `%v` would still include the
original text and could break the caller's matching contract.

---

## Placement of %w

> **Advisory**: Recommended best practice.

Place `%w` at the **end** of the error string so error text mirrors error chain
structure:

```go
// Good: %w at end - prints newest to oldest
err1 := fmt.Errorf("err1")
err2 := fmt.Errorf("err2: %w", err1)
err3 := fmt.Errorf("err3: %w", err2)
fmt.Println(err3) // err3: err2: err1
```

```go
// Bad: %w at start - prints oldest to newest (confusing)
err1 := fmt.Errorf("err1")
err2 := fmt.Errorf("%w: err2", err1)
err3 := fmt.Errorf("%w: err3", err2)
fmt.Println(err3) // err1: err2: err3
```

```go
// Bad: %w in middle - incoherent order
err1 := fmt.Errorf("err1")
err2 := fmt.Errorf("err2-1 %w err2-2", err1)
err3 := fmt.Errorf("err3-1 %w err3-2", err2)
fmt.Println(err3) // err3-1 err2-1 err1 err2-2 err3-2
```

**Pattern**: Use the form `context message: %w`

---

## Adding Information to Errors

> **Advisory**: Recommended best practice.

### Add Context, Not Redundancy

Add information that you have but the caller/callee might not. Avoid duplicating
information the underlying error already provides:

```go
// Good: Adds meaningful context
if _, err := os.ReadFile("settings.txt"); err != nil {
    return fmt.Errorf("load application settings: %w", err)
}
// Output: load application settings: open settings.txt: no such file or directory
```

```go
// Bad: Duplicates the filename
if _, err := os.ReadFile("settings.txt"); err != nil {
    return fmt.Errorf("could not open settings.txt: %w", err)
}
// Output: could not open settings.txt: open settings.txt: no such file or directory
```

### Don't Annotate Without Purpose

If the annotation only indicates failure without adding information, just return
the error:

```go
// Bad: Annotation adds nothing
return fmt.Errorf("failed: %v", err)

// Good: Just return the error
return err
```

---

## Logging Errors

> **Advisory**: Recommended best practice.

When you do log errors, use `log/slog` (Go 1.21+) with structured key-value
pairs and the appropriate level:

- **`slog.Error`**: Reserve for actionable issues that need investigation.
- **`slog.Warn`**: For issues that may need attention but aren't immediately
  actionable.
- **`slog.Debug`**: For development tracing — only emitted when the handler's
  level is set to `LevelDebug`.

```go
// Good: Structured logging with appropriate levels
for _, q := range queries {
    slog.Debug("handling query", "query", q)
    q.Run()
}

// Good: Guard expensive formatting behind a level check
if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
    slog.Debug("query plan", "explain", q.Explain())
}

// Bad: Expensive call evaluated even when debug logging is disabled
slog.Debug("query plan", "explain", q.Explain())
```

### Protect Sensitive Information

Be careful with PII (Personally Identifiable Information) in log messages. Many
log sinks are not appropriate for sensitive user data.

---

## Quick Reference

| Pattern | Guidance |
|---------|----------|
| `%v` | Add context without unwrapping when the caller contract permits it; also used for logging |
| `%w` | Add context while preserving causes required for programmatic inspection |
| External messages | Translate safe response text separately; neither verb redacts error text |
| `%w` placement | Always at the end: `"context: %w"` |
| Adding context | Add new info, don't duplicate existing info |
| Empty annotation | Just return `err` instead of `fmt.Errorf("failed: %v", err)` |
| Logging | Don't log and return; use appropriate log levels |
