-- args: id
SELECT id, event_id, email, lang, quantity, amount, state, expires_at, payment_id, checkout_url, ticket_number_from, updated_at FROM purchases WHERE id = ?
