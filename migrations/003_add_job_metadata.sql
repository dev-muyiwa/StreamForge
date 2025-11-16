-- Add metadata fields to jobs table
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS resolutions TEXT[];
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS peak_vmaf_score DECIMAL(5,2);
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS stream_url TEXT;
