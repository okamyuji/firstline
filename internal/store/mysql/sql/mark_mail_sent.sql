-- args: sent_at, id
UPDATE mail_outbox SET sent_at = ? WHERE id = ?
