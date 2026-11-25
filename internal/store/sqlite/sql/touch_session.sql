-- args: last_seen_at, id_hash
UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?
