# SSE Progress Tracking Implementation

## Overview

The StreamForge video processing API has been transformed from a synchronous, blocking system into an asynchronous, job-based system with real-time Server-Sent Events (SSE) progress tracking.

## What's New

### Architecture Changes

1. **Asynchronous Job Processing**
   - `/process` endpoint now returns immediately with a job ID (202 Accepted)
   - Video processing happens in the background
   - Jobs are tracked in PostgreSQL database with 7-day retention

2. **Real-Time Progress Updates**
   - Server-Sent Events (SSE) streaming at `/jobs/:id/stream`
   - JSON status endpoint at `/jobs/:id`
   - Progress percentages from 0-100% across all pipeline stages
   - Detailed status messages at each step

3. **Database-Backed Job Tracking**
   - PostgreSQL database stores job metadata and progress events
   - Persistent across server restarts
   - Automatic cleanup of jobs older than 7 days

## API Endpoints

### 1. POST /process
**Submit a video for processing**

**Request:**
```bash
curl -X POST http://localhost:8081/process \
  -F "video=@/path/to/video.mp4" \
  -F "key=my-video.mp4"
```

**Response:**
```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "message": "Video processing started"
}
```

**Status Code:** 202 Accepted

---

### 2. GET /jobs/:id
**Get job status as JSON**

**Request:**
```bash
curl http://localhost:8081/jobs/550e8400-e29b-41d4-a716-446655440000
```

**Response:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "key": "my-video.mp4",
  "bucket": "input",
  "status": "processing",
  "stage": "transcoding",
  "progress": 45,
  "workflow_id": "video-processing-my-video.mp4-1699564800",
  "created_at": "2025-11-16T10:30:00Z",
  "updated_at": "2025-11-16T10:32:00Z",
  "latest_events": [
    {
      "stage": "uploading",
      "progress": 10,
      "message": "File saved",
      "created_at": "2025-11-16T10:30:05Z"
    },
    {
      "stage": "transcoding",
      "progress": 45,
      "message": "Transcoding 720p...",
      "created_at": "2025-11-16T10:32:00Z"
    }
  ]
}
```

**Status Codes:**
- 200 OK - Job found
- 404 Not Found - Job does not exist
- 400 Bad Request - Invalid job ID format

---

### 3. GET /jobs/:id/stream
**Stream real-time progress updates via SSE**

**Request:**
```bash
curl -N http://localhost:8081/jobs/550e8400-e29b-41d4-a716-446655440000/stream
```

**Response Stream:**
```
event: status
data: {"job_id":"550e8400-...","status":"processing","stage":"transcoding","progress":45}

event: progress
data: {"job_id":"550e8400-...","stage":"transcoding","progress":50,"message":"Transcoded 2 resolutions","timestamp":"2025-11-16T10:32:15Z"}

event: progress
data: {"job_id":"550e8400-...","stage":"validation","progress":60,"message":"Validating video quality (VMAF)","timestamp":"2025-11-16T10:33:00Z"}

event: progress
data: {"job_id":"550e8400-...","stage":"packaging","progress":85,"message":"Creating HLS/DASH manifests","timestamp":"2025-11-16T10:34:00Z"}

event: progress
data: {"job_id":"550e8400-...","stage":"completed","progress":100,"message":"Processing complete","timestamp":"2025-11-16T10:35:00Z"}

event: done
data: {}
```

**SSE Event Types:**
- `status` - Initial job status
- `progress` - Progress update
- `done` - Job completed (connection closes)

**Connection:**
- Auto-reconnect supported
- Heartbeat sent every 30 seconds to keep connection alive
- Connection closes automatically when job completes or fails

---

### 4. GET /health
**Health check endpoint**

**Response:**
```json
{
  "status": "healthy",
  "service": "streamforge",
  "version": "1.0.0"
}
```

## Progress Stages

The video processing pipeline emits progress through the following stages:

| Stage | Progress % | Description |
|-------|-----------|-------------|
| **uploading** | 5-10% | Saving uploaded video file locally |
| **transcoding** | 30-50% | Converting video to multiple resolutions |
| **validation** | 60% | VMAF quality validation |
| **packaging** | 75-85% | Creating HLS/DASH manifests |
| **storage** | 90-95% | Uploading to cloud storage |
| **completed** | 100% | Processing finished successfully |

## Job Statuses

| Status | Description |
|--------|-------------|
| `pending` | Job created, waiting to start |
| `processing` | Job is currently being processed |
| `completed` | Job completed successfully |
| `failed` | Job failed with an error |

## Database Schema

### `jobs` Table
```sql
id UUID PRIMARY KEY
key VARCHAR(255)
bucket VARCHAR(255)
status VARCHAR(50) (pending/processing/completed/failed)
stage VARCHAR(50) (uploading/transcoding/validation/packaging/storage/completed)
progress INTEGER (0-100)
workflow_id VARCHAR(255)
error_message TEXT
result JSONB
created_at TIMESTAMP WITH TIME ZONE
updated_at TIMESTAMP WITH TIME ZONE
completed_at TIMESTAMP WITH TIME ZONE
```

### `progress_events` Table
```sql
id BIGSERIAL PRIMARY KEY
job_id UUID REFERENCES jobs(id) ON DELETE CASCADE
stage VARCHAR(50)
progress INTEGER
message TEXT
details JSONB
created_at TIMESTAMP WITH TIME ZONE
```

## Setup Instructions

### 1. Database Setup

**Create PostgreSQL database:**
```bash
createdb streamforge
```

**Configure database connection in `config.yaml`:**
```yaml
database:
  dsn: "postgres://postgres:password@localhost:5432/streamforge?sslmode=disable"
```

### 2. Start Temporal Server

```bash
temporal server start-dev
```

### 3. Run Migrations

Migrations are run automatically on startup. The following tables will be created:
- `jobs`
- `progress_events`

### 4. Start StreamForge Server

```bash
go run cmd/streamforge/main.go
```

Server will start on `http://localhost:8081`

## Frontend Integration

### JavaScript Example (EventSource)

```javascript
// Submit video for processing
async function uploadVideo(file) {
  const formData = new FormData();
  formData.append('video', file);
  formData.append('key', file.name);

  const response = await fetch('http://localhost:8081/process', {
    method: 'POST',
    body: formData
  });

  const { job_id } = await response.json();
  return job_id;
}

// Track progress with SSE
function trackProgress(jobId, onProgress, onComplete, onError) {
  const eventSource = new EventSource(`http://localhost:8081/jobs/${jobId}/stream`);

  eventSource.addEventListener('status', (event) => {
    const status = JSON.parse(event.data);
    console.log('Initial status:', status);
  });

  eventSource.addEventListener('progress', (event) => {
    const progress = JSON.Parse(event.data);
    onProgress(progress);
  });

  eventSource.addEventListener('done', () => {
    onComplete();
    eventSource.close();
  });

  eventSource.onerror = (error) => {
    onError(error);
    eventSource.close();
  };

  return eventSource;
}

// Usage
const file = document.querySelector('input[type="file"]').files[0];
const jobId = await uploadVideo(file);

const eventSource = trackProgress(
  jobId,
  (progress) => {
    console.log(`${progress.stage}: ${progress.progress}% - ${progress.message}`);
    updateProgressBar(progress.progress);
  },
  () => {
    console.log('Processing complete!');
    showSuccessMessage();
  },
  (error) => {
    console.error('Processing failed:', error);
    showErrorMessage();
  }
);
```

### React Example

```jsx
import { useState, useEffect } from 'react';

function VideoUploader() {
  const [progress, setProgress] = useState(0);
  const [stage, setStage] = useState('');
  const [message, setMessage] = useState('');

  const handleUpload = async (file) => {
    // Upload file
    const formData = new FormData();
    formData.append('video', file);

    const response = await fetch('http://localhost:8081/process', {
      method: 'POST',
      body: formData
    });

    const { job_id } = await response.json();

    // Track progress
    const eventSource = new EventSource(`http://localhost:8081/jobs/${job_id}/stream`);

    eventSource.addEventListener('progress', (event) => {
      const data = JSON.parse(event.data);
      setProgress(data.progress);
      setStage(data.stage);
      setMessage(data.message);
    });

    eventSource.addEventListener('done', () => {
      eventSource.close();
    });

    eventSource.onerror = () => {
      eventSource.close();
    };
  };

  return (
    <div>
      <input type="file" onChange={(e) => handleUpload(e.target.files[0])} />
      <div className="progress-bar">
        <div style={{ width: `${progress}%` }}>{progress}%</div>
      </div>
      <div>Stage: {stage}</div>
      <div>Message: {message}</div>
    </div>
  );
}
```

## Configuration

### Database Configuration

```yaml
database:
  dsn: "postgres://user:password@host:port/dbname?sslmode=disable"
```

### Retention Policy

Jobs are automatically cleaned up after 7 days. This is configured in `cmd/streamforge/main.go`:

```go
go runCleanupJob(ctx, jobManager, logger, 7*24*time.Hour, 24*time.Hour)
```

To change retention:
- First parameter: retention period (e.g., `3*24*time.Hour` for 3 days)
- Second parameter: cleanup interval (how often to run cleanup)

## Middleware & Features

### Enabled Middleware
- **Recovery** - Panic recovery
- **Request ID** - Unique ID for each request
- **Real IP** - Extract real client IP
- **Logger** - Request logging
- **Timeout** - 60-second request timeout
- **CORS** - Cross-origin requests enabled

### Background Jobs
- **Cleanup Job** - Runs every 24 hours to delete old jobs

## Testing

### Test with curl

```bash
# 1. Upload video
JOB_ID=$(curl -X POST http://localhost:8081/process \
  -F "video=@test.mp4" \
  | jq -r '.job_id')

# 2. Check status
curl http://localhost:8081/jobs/$JOB_ID

# 3. Stream progress
curl -N http://localhost:8081/jobs/$JOB_ID/stream
```

### Test with HTTPie

```bash
# Upload
http POST localhost:8081/process video@test.mp4

# Status
http localhost:8081/jobs/{job_id}

# Stream
http --stream localhost:8081/jobs/{job_id}/stream
```

## Error Handling

### Job Errors

If processing fails, the job status will be `failed` and include an error message:

```json
{
  "id": "...",
  "status": "failed",
  "error_message": "Transcoding failed: ffmpeg returned error code 1",
  "created_at": "...",
  "updated_at": "..."
}
```

### SSE Connection Errors

- Network errors: EventSource auto-reconnects
- Job not found: Returns 404
- Invalid job ID: Returns 400

## Performance Considerations

1. **Database Connection Pooling** - Uses pgxpool for efficient connection management
2. **SSE Broadcasting** - In-memory channel-based broadcasting (no database polling)
3. **Graceful Shutdown** - 30-second timeout for in-flight requests
4. **Parallel VMAF** - Quality validation runs in parallel for all resolutions
5. **Background Processing** - Video processing doesn't block HTTP responses

## Troubleshooting

### Database Connection Issues

```bash
# Check PostgreSQL is running
psql -U postgres -d streamforge -c "SELECT 1"

# Verify migrations
psql -U postgres -d streamforge -c "\dt"
```

### Temporal Connection Issues

```bash
# Verify Temporal is running
temporal workflow list

# Check worker is registered
temporal task-queue describe --task-queue video-processing
```

### SSE Not Receiving Updates

1. Check browser console for connection errors
2. Verify job ID is correct
3. Check server logs for activity emission
4. Ensure CORS is configured correctly

## Next Steps

- Add authentication/authorization
- Implement webhook callbacks
- Add job priority queues
- Metrics and monitoring (Prometheus/Grafana)
- Rate limiting
- Resume/retry failed jobs
