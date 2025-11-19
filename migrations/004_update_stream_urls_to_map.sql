-- Update stream_url to JSONB to store multiple URLs (auto + resolutions)
ALTER TABLE jobs DROP COLUMN IF EXISTS stream_url;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS stream_urls JSONB;
