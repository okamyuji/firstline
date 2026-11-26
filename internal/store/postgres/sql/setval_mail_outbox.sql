-- args: (none); run only when mail_outbox has rows, after rows were inserted with explicit ids
SELECT setval(pg_get_serial_sequence('mail_outbox', 'id'), MAX(id)) FROM mail_outbox
