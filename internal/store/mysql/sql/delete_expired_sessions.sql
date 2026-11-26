-- args: created_cutoff, seen_cutoff (deletes created_at <= created_cutoff or last_seen_at <= seen_cutoff)
DELETE FROM sessions WHERE created_at <= ? OR last_seen_at <= ?
