-- args: quantity, quantity, event_id, quantity
UPDATE event_sales SET held = held + ?, remaining = remaining - ? WHERE event_id = ? AND remaining >= ?
