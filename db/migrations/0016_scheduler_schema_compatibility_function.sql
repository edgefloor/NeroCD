CREATE OR REPLACE FUNCTION nerocd_scheduler_schema_compatible()
RETURNS boolean
LANGUAGE sql
STABLE
AS $function$
SELECT
    EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_leases' AND column_name='attempt' AND is_nullable='NO' AND column_default IS NULL)
    AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_leases' AND column_name='fence' AND is_nullable='NO' AND column_default IS NULL)
    AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='task_runs' AND column_name='next_attempt' AND is_nullable='NO' AND column_default IN ('1', '1::integer'))
    AND EXISTS (
        SELECT 1 FROM pg_constraint constraint_row
        JOIN pg_attribute next_attempt ON next_attempt.attrelid=constraint_row.conrelid AND next_attempt.attname='next_attempt'
        WHERE constraint_row.conrelid=to_regclass('task_runs')
            AND constraint_row.conname='task_runs_next_attempt_positive'
            AND constraint_row.contype='c'
            AND constraint_row.convalidated
            AND constraint_row.conkey=ARRAY[next_attempt.attnum]::smallint[]
            AND regexp_replace(pg_get_expr(constraint_row.conbin, constraint_row.conrelid, true), '[[:space:]]', '', 'g')='next_attempt>0'
    )
    AND EXISTS (
        SELECT 1 FROM pg_constraint constraint_row
        JOIN pg_attribute attempt ON attempt.attrelid=constraint_row.conrelid AND attempt.attname='attempt'
        WHERE constraint_row.conrelid=to_regclass('run_leases')
            AND constraint_row.conname='run_leases_attempt_positive'
            AND constraint_row.contype='c'
            AND constraint_row.convalidated
            AND constraint_row.conkey=ARRAY[attempt.attnum]::smallint[]
            AND regexp_replace(pg_get_expr(constraint_row.conbin, constraint_row.conrelid, true), '[[:space:]]', '', 'g')='attempt>0'
    )
    AND EXISTS (
        SELECT 1 FROM pg_constraint constraint_row
        JOIN pg_attribute fence ON fence.attrelid=constraint_row.conrelid AND fence.attname='fence'
        WHERE constraint_row.conrelid=to_regclass('run_leases')
            AND constraint_row.conname='run_leases_fence_nonempty'
            AND constraint_row.contype='c'
            AND constraint_row.convalidated
            AND constraint_row.conkey=ARRAY[fence.attnum]::smallint[]
            AND regexp_replace(pg_get_expr(constraint_row.conbin, constraint_row.conrelid, true), '[[:space:]]', '', 'g')='length(fence)>0'
    )
    AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='task_runs' AND column_name='claim_order_at' AND is_nullable='NO' AND column_default='clock_timestamp()')
    AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='runner_claim_cursors' AND column_name='runner_id' AND data_type='text' AND is_nullable='NO')
    AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='runner_claim_cursors' AND column_name='claim_order_at' AND data_type='timestamp with time zone' AND is_nullable='YES')
    AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='runner_claim_cursors' AND column_name='run_id' AND data_type='text' AND is_nullable='YES')
    AND EXISTS (
        SELECT 1 FROM pg_constraint constraint_row
        JOIN pg_attribute runner_id ON runner_id.attrelid=constraint_row.conrelid AND runner_id.attname='runner_id'
        WHERE constraint_row.conrelid=to_regclass('runner_claim_cursors')
            AND constraint_row.conname='runner_claim_cursors_pkey'
            AND constraint_row.contype='p'
            AND constraint_row.convalidated
            AND constraint_row.conkey=ARRAY[runner_id.attnum]::smallint[]
    )
    AND EXISTS (
        SELECT 1 FROM pg_constraint constraint_row
        JOIN pg_attribute claim_order ON claim_order.attrelid=constraint_row.conrelid AND claim_order.attname='claim_order_at'
        JOIN pg_attribute run_id ON run_id.attrelid=constraint_row.conrelid AND run_id.attname='run_id'
        WHERE constraint_row.conrelid=to_regclass('runner_claim_cursors')
            AND constraint_row.conname='runner_claim_cursors_tuple_complete'
            AND constraint_row.contype='c'
            AND constraint_row.convalidated
            AND constraint_row.conkey=ARRAY[claim_order.attnum, run_id.attnum]::smallint[]
            AND regexp_replace(pg_get_expr(constraint_row.conbin, constraint_row.conrelid, true), '[[:space:]]', '', 'g')='claim_order_atISNULLANDrun_idISNULLORclaim_order_atISNOTNULLANDrun_idISNOTNULL'
    )
    AND EXISTS (
        SELECT 1 FROM pg_index index_row
        JOIN pg_class index_relation ON index_relation.oid=index_row.indexrelid
        JOIN pg_class table_relation ON table_relation.oid=index_row.indrelid
        JOIN pg_namespace namespace_row ON namespace_row.oid=table_relation.relnamespace
        JOIN pg_am access_method ON access_method.oid=index_relation.relam
        JOIN pg_attribute run_id ON run_id.attrelid=table_relation.oid AND run_id.attname='run_id'
        JOIN pg_attribute attempt ON attempt.attrelid=table_relation.oid AND attempt.attname='attempt'
        WHERE namespace_row.nspname=current_schema()
            AND table_relation.relname='run_leases'
            AND index_relation.relname='run_leases_run_id_attempt_unique'
            AND access_method.amname='btree'
            AND index_row.indisunique AND index_row.indisvalid AND index_row.indisready
            AND index_row.indpred IS NULL AND index_row.indexprs IS NULL
            AND index_row.indnkeyatts=2 AND index_row.indnatts=2
            AND index_row.indkey[0]=run_id.attnum AND index_row.indkey[1]=attempt.attnum
    )
    AND EXISTS (
        SELECT 1 FROM pg_index index_row
        JOIN pg_class index_relation ON index_relation.oid=index_row.indexrelid
        JOIN pg_class table_relation ON table_relation.oid=index_row.indrelid
        JOIN pg_namespace namespace_row ON namespace_row.oid=table_relation.relnamespace
        JOIN pg_am access_method ON access_method.oid=index_relation.relam
        JOIN pg_attribute claim_order ON claim_order.attrelid=table_relation.oid AND claim_order.attname='claim_order_at'
        JOIN pg_attribute run_id ON run_id.attrelid=table_relation.oid AND run_id.attname='id'
        WHERE namespace_row.nspname=current_schema()
            AND table_relation.relname='task_runs'
            AND index_relation.relname='idx_task_runs_queued_claim_order_id'
            AND access_method.amname='btree'
            AND NOT index_row.indisunique
            AND index_row.indisvalid AND index_row.indisready
            AND index_row.indexprs IS NULL
            AND index_row.indnkeyatts=2 AND index_row.indnatts=2
            AND index_row.indkey[0]=claim_order.attnum AND index_row.indkey[1]=run_id.attnum
            AND regexp_replace(pg_get_expr(index_row.indpred, index_row.indrelid, true), '[[:space:]]', '', 'g')='status=''queued''::text'
    )
    AND to_regclass('runner_claim_cursors') IS NOT NULL;
$function$;
