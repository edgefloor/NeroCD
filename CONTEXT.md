# NeroCD domain glossary

These terms distinguish hosting resources from the later workspace and agent
product. A definition describes the domain model, not a claim that every concept
is implemented. See [current support](docs/compatibility.md) and the
[full target](docs/product-spec.md).

## Terms

**Task**:
A durable user goal with collaborators, source lineage, activity, review decisions
and linked workspaces, execution runs and environments.
_Avoid_: Run, generic task run, workspace

**Run**:
A bounded execution of work for a goal, with its own attempts and outcome.
_Avoid_: Task, workspace, environment

**Attempt**:
One authorized execution of a run on a selected worker.
_Avoid_: Retry task, replacement task

**Workspace**:
Mutable source and tool state with an identified current writer and checkpoint policy.
_Avoid_: Project environment, preview, release

**Checkpoint**:
An immutable recoverable work snapshot with a declared continuation limit.
_Avoid_: Live workspace, universal process snapshot, release

**Project environment**:
A project grouping of related running resources with an independent lifetime,
such as preview, staging or production.
_Avoid_: Workspace, legacy service environment, machine

**Resource**:
An independently operated application, database or stack in a project environment.
_Avoid_: Machine, execution run

**Machine**:
A stable physical or virtual host with declared roles, eligibility and availability.
_Avoid_: Agent process, reservation, destination

**Server**:
The existing hosting name for a stable enrolled host identity that must remain
recognizable when the product introduces Machine terminology.
_Avoid_: Agent process, disposable workspace

**Destination**:
A specific runtime boundary and data placement on a machine.
_Avoid_: Machine tag, project, interchangeable worker

**Reservation**:
An accounted allocation of capacity to accepted work or a persistent workload.
_Avoid_: Capability, permission, machine tag

**Task grant**:
Short-lived authorization for a specific run or supported attachment and its
permitted operations and resources.
_Avoid_: Agent identity, permanent project token

**Artifact**:
Immutable produced content with its identity, locations and retained evidence.
_Avoid_: Cache entry, mutable checkout

**Verification run**:
An execution of a versioned check against a named source or artifact subject.
_Avoid_: Agent narrative, build-cache hit

**Release**:
An immutable deployable artifact set with source and build evidence.
_Avoid_: Branch head, workspace, deployment attempt

**Deployment plan**:
A release bound to a named target, effective configuration, required evidence
and data preconditions.
_Avoid_: Release alone, historical approval

**Preview**:
A separately retained review environment tied to a task or source change.
_Avoid_: Agent session, mutable workspace, production

**Local User**:
A human user who authenticates directly to NeroCD with an email address and password.
_Avoid_: Account, operator account, API user

**API Token**:
A bearer credential used by automation or service accounts to call NeroCD APIs without a Local User password.
_Avoid_: Session, runner token, password

**Runner Token**:
A bearer credential assigned to a runner so it can heartbeat, claim work, report logs and artifacts, and complete leases.
_Avoid_: API token, session token, password
