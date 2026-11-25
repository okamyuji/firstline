-- args: new_state, updated_at, id, old_state
UPDATE purchases SET state = ?, updated_at = ? WHERE id = ? AND state = ?
