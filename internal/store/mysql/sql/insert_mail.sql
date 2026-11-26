-- args: purchase_id, kind, lang
INSERT INTO mail_outbox (purchase_id, kind, lang, sent_at) VALUES (?, ?, ?, NULL)
