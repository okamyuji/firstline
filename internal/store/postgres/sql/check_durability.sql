-- args: (none); returns each setting that lets a commit return before the WAL reaches the disk
SELECT name, setting FROM pg_settings WHERE name IN ('synchronous_commit', 'fsync') AND setting <> 'on'
