# Failure-boundary review notes

Read this only when the diff changes a listed boundary. These are review prompts,
not universal design requirements.

## Structured results and partial data

When a model or protocol response includes a terminal failure status, stop
follow-on tool scheduling in both the live path and any replay/resume path.
Retain partial data only when the API documents it as usable; communicate the
incomplete result instead of presenting full success. Check the error-plus-result
contract before changing either behavior.

## Capacity and reloads

Review synchronous waiter count, time spent waiting, and active execution slots
as distinct budgets. A rejected request must not start work, and cancellation
must release capacity. For reloads, distinguish desired from loaded generation
and retain an older known-good value only when the established readiness
contract allows it.

## Files, signals, and configuration

Allocate per-run log files atomically and uniquely, unless a documented shared
stream is intentional. Exercise OS signal shutdown with a subprocess that
reports it has started, then use bounded cleanup. Decode YAML or another
structured format with its real decoder; cover format-applicable values that
line splitting loses and enforce the schema's actual unknown-field policy.

## Source examples

These examples motivate the conditional prompts above; they do not import the
foreign projects' architecture or prescribe their implementation.

- [unreal-agent terminal response failure](https://github.com/edgefloor/unreal-agent/commit/bb4e33f06062e162f45ada624018eb192454b197)
- [unreal-agent unique datetime logs](https://github.com/edgefloor/unreal-agent/commit/b8e43856ba4838d9ecfd4146fdd90fa3bded3d26)
- [unreal-agent SIGTERM shutdown](https://github.com/edgefloor/unreal-agent/commit/b26b3f849d660201b8ad462fe38e84be79efa333)
- [unreal-agent YAML decoder](https://github.com/edgefloor/unreal-agent/commit/d4527702ee2385c8d2d969f961ae8a125566b906)
- [cloudattrib waiter limits](https://github.com/edgefloor/cloudattrib/commit/29db2f747e9253693315a886ec68eebfb678a307)
- [cloudattrib retained partial responses](https://github.com/edgefloor/cloudattrib/commit/d578eb3781473f962d56ba56858a336643d0914a)
- [cloudattrib reload generations](https://github.com/edgefloor/cloudattrib/commit/66eaf2b845d10d000331f76f2e5ee7a9bc1a3923)
