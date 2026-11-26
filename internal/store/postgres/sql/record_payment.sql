-- args: payment_id, checkout_url, id
UPDATE purchases SET payment_id = ?, checkout_url = ? WHERE id = ? AND payment_id IS NULL
