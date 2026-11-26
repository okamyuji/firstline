-- args: purchase_id, payment_id, kind, amount, at
INSERT INTO billing_logs (purchase_id, payment_id, kind, amount, at) VALUES (?, ?, ?, ?, ?)
