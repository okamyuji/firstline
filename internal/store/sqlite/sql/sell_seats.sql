-- args: quantity, quantity, event_id
UPDATE event_sales SET held = held - ?, sold = sold + ? WHERE event_id = ?
