-- args: cutoff (unix ms; held purchases with expires_at <= cutoff)
SELECT id, event_id, quantity, expires_at FROM purchases WHERE state = 'held' AND expires_at <= ? ORDER BY expires_at, id
