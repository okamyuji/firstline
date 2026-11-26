-- args: quantity, id (run after advance_ticket_number in the same transaction)
SELECT next_ticket_number - ? FROM events WHERE id = ?
