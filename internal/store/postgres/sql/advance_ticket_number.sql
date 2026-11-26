-- args: quantity, id
UPDATE events SET next_ticket_number = next_ticket_number + ? WHERE id = ?
