-- Create jobs table for tracking video processing jobs
CREATE TABLE IF NOT EXISTS jobs (
    id UUID PRIMARY KEY,
    key VARCHAR(255) NOT NULL,
    bucket VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    stage VARCHAR(50),
    progress INTEGER NOT NULL DEFAULT 0,
    workflow_id VARCHAR(255),
    error_message TEXT,
    result JSONB,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP WITH TIME ZONE
);

-- Create index on status for filtering
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

-- Create index on created_at for cleanup queries
CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at);

-- Create index on workflow_id for lookups
CREATE INDEX IF NOT EXISTS idx_jobs_workflow_id ON jobs(workflow_id);
