INSERT INTO users (id, email, name, status, global_role, password_hash)
VALUES
    ('usr_bootstrap', 'admin@example.local', 'Bootstrap Admin', 'active', 'system_admin', 'sha256:8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918'),
    ('usr_viewer', 'viewer@example.local', 'Security Viewer', 'active', 'user', 'sha256:d35ca5051b82ffc326a3b0b6574a9a3161dee16b9478a199ee39cd803ce5b799')
ON CONFLICT (id) DO UPDATE SET password_hash = EXCLUDED.password_hash, global_role = EXCLUDED.global_role;

INSERT INTO projects (id, name, description)
VALUES
    ('proj_platform', 'Platform Automation', 'Shared infrastructure runbooks and deployments.'),
    ('proj_security', 'Security Operations', 'Audited response and compliance automation.')
ON CONFLICT (id) DO NOTHING;

INSERT INTO repositories (id, project_id, name, url, provider, default_ref)
VALUES
    ('repo_platform_runbooks', 'proj_platform', 'Platform Runbooks', 'https://example.local/platform/runbooks.git', 'git', 'main'),
    ('repo_security_runbooks', 'proj_security', 'Security Runbooks', 'https://example.local/security/runbooks.git', 'git', 'main')
ON CONFLICT (id) DO NOTHING;

INSERT INTO access_keys (id, project_id, name, kind, fingerprint)
VALUES
    ('key_ansible_vault', 'proj_platform', 'Ansible Vault', 'password', 'sha256:seed-ansible-vault'),
    ('key_token_admin', 'proj_security', 'Token Admin', 'password', 'sha256:seed-token-admin')
ON CONFLICT (id) DO NOTHING;

INSERT INTO inventories (id, project_id, name, kind, source)
VALUES
    ('inv_platform_prod', 'proj_platform', 'Platform Production', 'static', 'inventories/prod.ini'),
    ('inv_security_response', 'proj_security', 'Security Response', 'static', 'inventories/response.ini')
ON CONFLICT (id) DO NOTHING;

INSERT INTO project_members (id, project_id, user_id, role)
VALUES
    ('pm_proj_platform_usr_bootstrap', 'proj_platform', 'usr_bootstrap', 'owner'),
    ('pm_proj_security_usr_bootstrap', 'proj_security', 'usr_bootstrap', 'owner'),
    ('pm_proj_security_usr_viewer', 'proj_security', 'usr_viewer', 'viewer')
ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = now();

INSERT INTO task_templates (id, project_id, name, kind, run_spec, workflow, runner_tags, requires_ack)
VALUES
    ('tpl_patch', 'proj_platform', 'Patch Linux Fleet', 'ansible', '{"type":"ansible","inputs":{"playbook":"patch.yml"},"repository":{"id":"repo_platform_runbooks","ref":"main","path":"ansible"},"process":{"command":["ansible-playbook","patch.yml"],"timeout_seconds":1800},"artifacts":[{"name":"patch-report","path":"reports/patch.json","required":false}],"secrets":[{"name":"ansible-vault","provider":"database","reference":"sec_ansible_vault","target":"env:ANSIBLE_VAULT_PASSWORD"}]}'::jsonb, '{"steps":[{"id":"checkout","name":"Checkout runbooks","run_spec":{"type":"shell","inputs":{"command":"git checkout"}}},{"id":"patch","name":"Patch fleet","depends_on":["checkout"],"requires_ack":true,"run_spec":{"type":"ansible","inputs":{"playbook":"patch.yml"}}}]}'::jsonb, ARRAY['linux', 'prod'], true),
    ('tpl_plan', 'proj_platform', 'OpenTofu Plan', 'opentofu', '{"type":"opentofu","inputs":{"command":"plan"},"repository":{"id":"repo_platform_runbooks","ref":"main","path":"tofu"},"process":{"command":["tofu","plan","-out=tfplan"],"timeout_seconds":1200},"artifacts":[{"name":"tfplan","path":"tfplan","required":true}]}'::jsonb, '{"steps":[{"id":"checkout","name":"Checkout IaC","run_spec":{"type":"shell","inputs":{"command":"git checkout"}}},{"id":"plan","name":"OpenTofu plan","depends_on":["checkout"],"run_spec":{"type":"opentofu","inputs":{"command":"plan"}}}]}'::jsonb, ARRAY['tofu'], false),
    ('tpl_rotate', 'proj_security', 'Rotate Service Tokens', 'shell', '{"type":"shell","inputs":{"command":"./rotate-tokens.sh"},"repository":{"id":"repo_security_runbooks","ref":"main","path":"tokens"},"process":{"command":["./rotate-tokens.sh"],"timeout_seconds":600},"secrets":[{"name":"token-admin","provider":"database","reference":"sec_token_admin","target":"env:TOKEN_ADMIN"}]}'::jsonb, '{"steps":[{"id":"checkout","name":"Checkout security runbooks","run_spec":{"type":"shell","inputs":{"command":"git checkout"}}},{"id":"rotate","name":"Rotate tokens","depends_on":["checkout"],"requires_ack":true,"run_spec":{"type":"shell","inputs":{"command":"./rotate-tokens.sh"}}}]}'::jsonb, ARRAY['secure'], true)
ON CONFLICT (id) DO NOTHING;

INSERT INTO task_runs (id, project_id, template_id, run_spec, workflow, runner_tags, status, requested_by, started_at, finished_at)
VALUES
    ('run_001', 'proj_platform', 'tpl_plan', '{"type":"opentofu","inputs":{"command":"plan"},"repository":{"id":"repo_platform_runbooks","ref":"main","path":"tofu"},"process":{"command":["tofu","plan","-out=tfplan"],"timeout_seconds":1200},"artifacts":[{"name":"tfplan","path":"tfplan","required":true}]}'::jsonb, '{"steps":[{"id":"checkout","name":"Checkout IaC","run_spec":{"type":"shell","inputs":{"command":"git checkout"}}},{"id":"plan","name":"OpenTofu plan","depends_on":["checkout"],"run_spec":{"type":"opentofu","inputs":{"command":"plan"}}}]}'::jsonb, ARRAY['tofu'], 'succeeded', 'usr_bootstrap', now() - interval '22 minutes', now() - interval '20 minutes'),
    ('run_002', 'proj_security', 'tpl_rotate', '{"type":"shell","inputs":{"command":"./rotate-tokens.sh"},"repository":{"id":"repo_security_runbooks","ref":"main","path":"tokens"},"process":{"command":["./rotate-tokens.sh"],"timeout_seconds":600},"secrets":[{"name":"token-admin","provider":"database","reference":"sec_token_admin","target":"env:TOKEN_ADMIN"}]}'::jsonb, '{"steps":[{"id":"checkout","name":"Checkout security runbooks","run_spec":{"type":"shell","inputs":{"command":"git checkout"}}},{"id":"rotate","name":"Rotate tokens","depends_on":["checkout"],"requires_ack":true,"run_spec":{"type":"shell","inputs":{"command":"./rotate-tokens.sh"}}}]}'::jsonb, ARRAY['secure'], 'waiting_approval', 'usr_bootstrap', now() - interval '5 minutes', NULL)
ON CONFLICT (id) DO NOTHING;

INSERT INTO approvals (id, run_id, status, requested_by, created_at)
VALUES ('apr_002', 'run_002', 'pending', 'usr_bootstrap', now() - interval '5 minutes')
ON CONFLICT (id) DO NOTHING;

INSERT INTO run_logs (id, run_id, sequence, stream, message, created_at)
VALUES
    ('log_001', 'run_001', 1, 'stdout', 'Initializing OpenTofu working directory', now() - interval '22 minutes'),
    ('log_002', 'run_001', 2, 'stdout', 'Plan completed with no destructive changes', now() - interval '21 minutes'),
    ('log_003', 'run_002', 1, 'system', 'Run is waiting for an authorized approval', now() - interval '5 minutes')
ON CONFLICT (id) DO NOTHING;
