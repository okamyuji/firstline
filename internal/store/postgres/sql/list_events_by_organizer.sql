-- args: organizer_id
SELECT e.id, e.title_ja, e.title_en, e.starts_at, e.ends_at, e.price, e.sale_starts_at, s.remaining FROM events e JOIN event_sales s ON s.event_id = e.id WHERE e.organizer_id = ? ORDER BY e.starts_at, e.id
