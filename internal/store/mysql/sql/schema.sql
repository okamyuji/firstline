-- utf8mb4_bin compares ids, emails and states byte by byte, as SQLite and PostgreSQL do.
-- MySQL has no CREATE INDEX IF NOT EXISTS, so the indexes live inside CREATE TABLE IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS organizers (
    id VARCHAR(64) PRIMARY KEY,
    email VARCHAR(254) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS sessions (
    id_hash VARCHAR(64) PRIMARY KEY,
    organizer_id VARCHAR(64) NOT NULL,
    created_at BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS events (
    id VARCHAR(64) PRIMARY KEY,
    organizer_id VARCHAR(64) NOT NULL,
    title_ja TEXT NOT NULL,
    title_en TEXT NOT NULL,
    starts_at BIGINT NOT NULL,
    ends_at BIGINT NOT NULL,
    capacity INTEGER NOT NULL,
    price BIGINT NOT NULL,
    sale_starts_at BIGINT NOT NULL,
    next_ticket_number INTEGER NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS purchases (
    id VARCHAR(64) PRIMARY KEY,
    event_id VARCHAR(64) NOT NULL,
    email VARCHAR(254) NOT NULL,
    lang VARCHAR(16) NOT NULL,
    quantity INTEGER NOT NULL,
    amount BIGINT NOT NULL,
    state VARCHAR(16) NOT NULL,
    expires_at BIGINT NOT NULL,
    payment_id VARCHAR(255),
    checkout_url TEXT,
    ticket_number_from INTEGER,
    updated_at BIGINT NOT NULL,
    INDEX purchases_state_expires_at (state, expires_at),
    INDEX purchases_event_email_state (event_id, email, state)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS event_sales (
    event_id VARCHAR(64) PRIMARY KEY,
    held INTEGER NOT NULL,
    sold INTEGER NOT NULL,
    remaining INTEGER NOT NULL,
    refunds INTEGER NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS orphan_refunds (
    payment_id VARCHAR(255) PRIMARY KEY,
    purchase_id VARCHAR(255) NOT NULL,
    amount BIGINT NOT NULL,
    state VARCHAR(16) NOT NULL,
    updated_at BIGINT NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS mail_outbox (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    purchase_id VARCHAR(64) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    lang VARCHAR(16) NOT NULL,
    sent_at BIGINT,
    INDEX mail_outbox_sent_at_id (sent_at, id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS billing_logs (
    purchase_id VARCHAR(255) NOT NULL,
    payment_id VARCHAR(255) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    amount BIGINT NOT NULL,
    at BIGINT NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

CREATE TABLE IF NOT EXISTS access_logs (
    method VARCHAR(16) NOT NULL,
    path TEXT NOT NULL,
    status INTEGER NOT NULL,
    duration_ms BIGINT NOT NULL,
    at BIGINT NOT NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;
