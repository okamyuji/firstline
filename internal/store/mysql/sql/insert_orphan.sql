-- args: payment_id, purchase_id, amount, updated_at
-- The no-op update affects 0 rows on a duplicate unless clientFoundRows is on; INSERT IGNORE would also turn other errors into warnings.
INSERT INTO orphan_refunds (payment_id, purchase_id, amount, state, updated_at) VALUES (?, ?, ?, 'refunding', ?) ON DUPLICATE KEY UPDATE payment_id = payment_id
