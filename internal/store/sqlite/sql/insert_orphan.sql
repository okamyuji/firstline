-- args: payment_id, purchase_id, amount, updated_at
INSERT INTO orphan_refunds (payment_id, purchase_id, amount, state, updated_at) VALUES (?, ?, ?, 'refunding', ?) ON CONFLICT (payment_id) DO NOTHING
