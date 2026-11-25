-- args: (none)
SELECT m.id, m.purchase_id, m.kind, m.lang, p.state, p.payment_id, p.checkout_url, p.email, p.quantity, p.amount, p.expires_at, p.ticket_number_from, e.title_ja, e.title_en, e.starts_at FROM mail_outbox m JOIN purchases p ON p.id = m.purchase_id JOIN events e ON e.id = p.event_id WHERE m.sent_at IS NULL ORDER BY m.id ASC
