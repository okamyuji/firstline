-- args: event_id
SELECT event_id, held, sold, remaining, refunds FROM event_sales WHERE event_id = ?
