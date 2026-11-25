-- args: event_id, email
SELECT COUNT(*) FROM purchases WHERE event_id = ? AND email = ? AND state = 'held'
