-- args: quantity, quantity, event_id
UPDATE event_sales SET held = held - ?, remaining = remaining + ? WHERE event_id = ?
