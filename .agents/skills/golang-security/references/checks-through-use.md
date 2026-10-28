# Checks through use

Use the sections relevant to the changed boundary. For each, identify the checked
object, the sensitive operation, and what can change between them. Follow wrappers,
subprocesses, retries, and replay to the operation that actually consumes the data.
Report a defect only with a reachable path and an observable consequence.

## Files and destinations

- A validated pathname is not a retained file identity. When replacement is possible,
  retain the admitted directory and opened file through verification, consumption,
  publication, and cleanup. Use the verified descriptor as subprocess input where
  supported. Rechecking then reopening leaves another replacement window.
- Root confinement and object identity are separate guarantees. `os.Root` confines
  lookup; a later lookup can still select a different entry inside that root. Wipe
  the retained file, and do not unlink a replacement entry during cleanup. An
  identity check followed by unlink is not atomic against a concurrent writer;
  use an exclusively controlled directory or document the remaining constraint.
- Create private temporary files exclusively. A random staging file does not make
  a later truncating write to a predictable destination safe. Inspect publication
  and failure cleanup too, including shell scripts and sourced helpers.
- For outbound destination restrictions, validate all resolved addresses and bind
  the connection to an approved address while preserving the intended TLS hostname.
  A separate DNS check does not constrain a client or Git subprocess's later lookup.
  Inspect redirects, proxies, and fallback transports; reject unsupported enforcement.
  Where HTTPS is required, redirect chains must not downgrade to HTTP.

## Authority and admission

- Authorize the resource belonging to the stored operation before returning replay
  or idempotent results. An authorized resource supplied with the new request does
  not authorize another resource referenced by that operation.
- Check every execution entry point against the same supported capabilities.
  Generic dispatch must not bypass typed deployment authority or silently ignore
  requested approval semantics. Reject unsupported behavior before preparation or
  other side effects, not only before final execution.
- A concurrent limit needs atomic reservation before protected work begins, then
  settlement on success, failure, and cancellation. A mutex around separate check
  and update calls does not reserve capacity. One attempt must not release another
  pending attempt's reservation.

## Representations and secrets

- Shell-safe arguments can still expose secrets in process arguments. Trace bearer
  headers, passwords, and request bodies through command invocations; use an
  appropriate private file or descriptor rather than putting them in argv. Check
  permissions, lifetime, cleanup, and debug output of that transport.
- Preserve raw bytes until masking. Trimming CR/LF, decoding, or serializing first
  can change a secret so it no longer matches. Exercise overlapping secrets and
  arbitrary read boundaries, then verify durable storage and delivery as well as
  the redactor. After masking, preserve incomplete UTF-8 across text event frames.
- Preserve structure across boundaries. Delimiter-joined keys can collide when
  fields contain that delimiter; use structured keys or an unambiguous encoding.
  Keep untrusted text out of HTML interpretation, including fallback interfaces.

## Work limits and evidence

- Enforce size, count, and depth budgets before each expansion or expensive step.
  Rejecting an oversized final result does not bound the allocation that produced
  it. Avoid rescanning all descendants from every node in a nested structure.
- Choose a falsifying test for the changed boundary: replace the path after admission,
  redirect an approved destination, replay after access revocation, or synchronize
  concurrent attempts before settlement. Assert the protected operation did not
  escape, leak, or start, not just that a validator returned an error.
- Use deterministic barriers or controlled transports for ordering tests. For
  complexity changes, use scaling evidence or bounded work measurements rather
  than fragile tight wall-clock assertions. Retain a valid-path assertion so that
  rejecting all inputs cannot satisfy the test.
