-- args: (none); returns each setting that lets a commit return before the WAL reaches the disk.
-- remote_apply waits for the local flush like on, and for a synchronous standby as well.
SELECT name, setting FROM pg_settings WHERE name IN ('synchronous_commit', 'fsync') AND setting NOT IN ('on', 'remote_apply')
