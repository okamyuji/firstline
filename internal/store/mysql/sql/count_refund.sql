-- args: event_id
UPDATE event_sales SET refunds = refunds + 1 WHERE event_id = ?
