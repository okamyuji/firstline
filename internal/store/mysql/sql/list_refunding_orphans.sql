-- args: (none)
SELECT payment_id, purchase_id, amount FROM orphan_refunds WHERE state = 'refunding' ORDER BY payment_id
