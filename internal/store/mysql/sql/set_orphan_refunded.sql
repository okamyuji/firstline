-- args: updated_at, payment_id
UPDATE orphan_refunds SET state = 'refunded', updated_at = ? WHERE payment_id = ? AND state = 'refunding'
