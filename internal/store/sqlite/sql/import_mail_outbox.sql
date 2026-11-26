-- args: id, purchase_id, kind, lang, sent_at
INSERT INTO mail_outbox (id, purchase_id, kind, lang, sent_at) VALUES (?, ?, ?, ?, ?)
