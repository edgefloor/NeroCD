-- Maintainer cancellation is a deployment control-plane operation, not a
-- generic task-run mutation. The receipt is stable across a lost response.
CREATE TABLE deployment_cancellations (
 deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL,
 actor_id TEXT NOT NULL REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (deployment_id, request_id),
 UNIQUE (deployment_id)
);

CREATE INDEX deployment_cancellations_actor ON deployment_cancellations(actor_id, created_at);
