# NeroCD

NeroCD is an automation operations platform for controlling projects, task runs,
runners, approvals, logs, and audit trails.

## Language

**Local User**:
A human user who authenticates directly to NeroCD with an email address and password.
_Avoid_: Account, operator account, API user

**API Token**:
A bearer credential used by automation or service accounts to call NeroCD APIs without a Local User password.
_Avoid_: Session, runner token, password

**Runner Token**:
A bearer credential assigned to a runner so it can heartbeat, claim work, report logs and artifacts, and complete leases.
_Avoid_: API token, session token, password
