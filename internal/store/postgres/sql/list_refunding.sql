-- args: (none)
SELECT id, payment_id, amount FROM purchases WHERE state = 'refunding' AND payment_id IS NOT NULL ORDER BY id
