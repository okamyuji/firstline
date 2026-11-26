-- args: now (events that have not started)
SELECT e.id, e.title_ja, e.title_en, e.starts_at, e.ends_at, e.price, e.sale_starts_at, s.remaining FROM events e JOIN event_sales s ON s.event_id = e.id WHERE e.starts_at > ? ORDER BY e.starts_at, e.id
