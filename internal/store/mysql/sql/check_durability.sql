-- args: (none); returns the setting when a commit can return before the redo log reaches the disk
SELECT 'innodb_flush_log_at_trx_commit' AS name, CAST(@@GLOBAL.innodb_flush_log_at_trx_commit AS CHAR) AS setting FROM DUAL WHERE @@GLOBAL.innodb_flush_log_at_trx_commit <> 1
