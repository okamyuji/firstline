-- args: id, event_id, email, lang, quantity, amount, expires_at, updated_at
INSERT INTO purchases (id, event_id, email, lang, quantity, amount, state, expires_at, payment_id, checkout_url, ticket_number_from, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'held', ?, NULL, NULL, NULL, ?)
