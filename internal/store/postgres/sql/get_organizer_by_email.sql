-- args: email
SELECT id, email, password_hash FROM organizers WHERE email = ?
