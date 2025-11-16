-- Create progress_events table for detailed stage tracking
CREATE TABLE IF NOT EXISTS progress_events (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    stage VARCHAR(50) NOT NULL,
    progress INTEGER NOT NULL,
    message TEXT NOT NULL,
    details JSONB,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Create index on job_id for efficient lookups
CREATE INDEX IF NOT EXISTS idx_progress_events_job_id ON progress_events(job_id);

-- Create index on job_id and created_at for ordered queries
CREATE INDEX IF NOT EXISTS idx_progress_events_job_created ON progress_events(job_id, created_at DESC);
