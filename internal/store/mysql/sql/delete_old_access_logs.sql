-- args: cutoff (deletes at < cutoff)
DELETE FROM access_logs WHERE at < ?
