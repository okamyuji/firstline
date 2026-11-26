-- args: payment_id, purchase_id, amount, state, updated_at
INSERT INTO orphan_refunds (payment_id, purchase_id, amount, state, updated_at) VALUES (?, ?, ?, ?, ?)
