-- args: id_hash
SELECT id_hash, organizer_id, created_at, last_seen_at FROM sessions WHERE id_hash = ?
