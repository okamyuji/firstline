-- args: (none)
SELECT e.id AS event_id, e.capacity, COALESCE(SUM(p.quantity), 0) AS active FROM events e LEFT JOIN purchases p ON p.event_id = e.id AND p.state IN ('held', 'purchased') GROUP BY e.id, e.capacity ORDER BY e.id
