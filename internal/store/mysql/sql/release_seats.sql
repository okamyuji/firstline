-- args: quantity, quantity, event_id, quantity (no row changes when fewer than quantity seats are held)
UPDATE event_sales SET held = held - ?, remaining = remaining + ? WHERE event_id = ? AND held >= ?
