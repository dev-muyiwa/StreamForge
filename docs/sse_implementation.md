# Server-Sent Events (SSE) Implementation Guide

## Overview

This document explains the robust SSE implementation for streaming video processing progress updates in real-time.

## Backend Implementation

### Key Fixes Applied

The SSE endpoint has been completely rewritten to eliminate `ERR_INCOMPLETE_CHUNKED_ENCODING` errors and connection instability. Here are the critical fixes:

#### 1. **Explicit Status Code Writing**
```go
// CRITICAL: Write status code 200 explicitly before sending data
w.WriteHeader(http.StatusOK)
flusher.Flush()
```
**Why**: Without explicitly writing the status code, Go may buffer the response, causing chunked encoding errors.

#### 2. **X-Accel-Buffering Header**
```go
w.Header().Set("X-Accel-Buffering", "no")
```
**Why**: Nginx and other reverse proxies buffer responses by default. This header disables buffering, which is essential for SSE.

#### 3. **Reduced Heartbeat Interval**
```go
heartbeat := time.NewTicker(15 * time.Second)
```
**Why**: Previous 30-second interval was too long. Most proxies/load balancers have 30-60 second timeouts. 15 seconds keeps the connection alive reliably.

#### 4. **Immediate Flushing After Every Write**
```go
fmt.Fprint(w, message)
flusher.Flush()  // Flush IMMEDIATELY after every write
```
**Why**: Buffered data causes incomplete chunked encoding errors. Every write must be flushed immediately.

#### 5. **Increased Channel Buffer**
```go
ch := make(chan ProgressUpdate, 50)  // Increased from 10 to 50
```
**Why**: Rapid progress updates (especially during transcoding) can overwhelm a small buffer, causing the emitter to block.

#### 6. **Proper Error Detection**
```go
n, err := fmt.Fprint(w, message)
if err != nil {
    return fmt.Errorf("failed to write: %w", err)
}
if n == 0 {
    return fmt.Errorf("wrote 0 bytes")
}
```
**Why**: Detects connection failures immediately and closes gracefully instead of attempting to write to a closed connection.

#### 7. **Graceful Completion**
```go
// Send final event
h.sendCompletionEvent(w, flusher, update)
// Wait 100ms for client to receive
time.Sleep(100 * time.Millisecond)
// Then close
return
```
**Why**: Ensures the client receives the final event before the connection closes.

### Event Types

The SSE endpoint emits four types of events:

1. **`status`** - Initial connection event with current job state
2. **`progress`** - Regular progress updates during processing
3. **`complete`** - Job completed successfully
4. **`error`** - Job failed

### Heartbeat Mechanism

- **Interval**: 15 seconds
- **Format**: SSE comment (`: heartbeat\n\n`)
- **Purpose**: Keeps connection alive through proxies and load balancers
- **Client Impact**: None - comments are ignored by EventSource API

## Frontend Implementation

### JavaScript EventSource Example

```javascript
class JobProgressTracker {
  constructor(jobId) {
    this.jobId = jobId;
    this.eventSource = null;
    this.reconnectAttempts = 0;
    this.maxReconnectAttempts = 5;
    this.reconnectDelay = 1000; // Start with 1 second
  }

  start(onProgress, onComplete, onError) {
    const url = `/jobs/${this.jobId}/stream`;

    this.eventSource = new EventSource(url);

    // Handle initial status
    this.eventSource.addEventListener('status', (e) => {
      const data = JSON.parse(e.data);
      console.log('Connected to progress stream:', data);
      onProgress(data);
      this.reconnectAttempts = 0; // Reset on successful connection
    });

    // Handle progress updates
    this.eventSource.addEventListener('progress', (e) => {
      const data = JSON.parse(e.data);
      console.log('Progress update:', data);
      onProgress(data);
    });

    // Handle completion
    this.eventSource.addEventListener('complete', (e) => {
      const data = JSON.parse(e.data);
      console.log('Job completed:', data);
      onComplete(data);
      this.stop();
    });

    // Handle errors
    this.eventSource.addEventListener('error', (e) => {
      const data = JSON.parse(e.data);
      console.error('Job error:', data);
      onError(data);
      this.stop();
    });

    // Handle connection errors
    this.eventSource.onerror = (e) => {
      console.error('SSE connection error:', e);

      // EventSource automatically reconnects, but we want to limit attempts
      if (this.eventSource.readyState === EventSource.CLOSED) {
        this.handleReconnect(onProgress, onComplete, onError);
      }
    };

    // Handle page unload
    window.addEventListener('beforeunload', () => this.stop());
  }

  handleReconnect(onProgress, onComplete, onError) {
    if (this.reconnectAttempts >= this.maxReconnectAttempts) {
      console.error('Max reconnection attempts reached');
      onError({ message: 'Connection lost, max retries exceeded' });
      return;
    }

    this.reconnectAttempts++;
    const delay = this.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1);

    console.log(`Reconnecting in ${delay}ms (attempt ${this.reconnectAttempts}/${this.maxReconnectAttempts})`);

    setTimeout(() => {
      this.stop();
      this.start(onProgress, onComplete, onError);
    }, delay);
  }

  stop() {
    if (this.eventSource) {
      this.eventSource.close();
      this.eventSource = null;
    }
  }
}

// Usage
const tracker = new JobProgressTracker('your-job-id-here');

tracker.start(
  // onProgress
  (data) => {
    document.getElementById('progress-bar').style.width = `${data.progress}%`;
    document.getElementById('stage').textContent = data.stage;
    document.getElementById('message').textContent = data.message;

    // Display detailed info if available
    if (data.details) {
      console.log('Details:', data.details);
    }
  },

  // onComplete
  (data) => {
    console.log('Processing complete!');
    document.getElementById('status').textContent = 'Complete';
  },

  // onError
  (data) => {
    console.error('Processing failed:', data.message);
    document.getElementById('status').textContent = 'Failed';
  }
);
```

### React Example

```jsx
import { useEffect, useState, useRef } from 'react';

function useJobProgress(jobId) {
  const [progress, setProgress] = useState({
    stage: 'pending',
    progress: 0,
    message: 'Waiting...',
  });
  const [isComplete, setIsComplete] = useState(false);
  const [error, setError] = useState(null);
  const eventSourceRef = useRef(null);

  useEffect(() => {
    if (!jobId) return;

    const eventSource = new EventSource(`/jobs/${jobId}/stream`);
    eventSourceRef.current = eventSource;

    eventSource.addEventListener('status', (e) => {
      const data = JSON.parse(e.data);
      setProgress(data);
    });

    eventSource.addEventListener('progress', (e) => {
      const data = JSON.parse(e.data);
      setProgress(data);
    });

    eventSource.addEventListener('complete', (e) => {
      const data = JSON.parse(e.data);
      setProgress(data);
      setIsComplete(true);
      eventSource.close();
    });

    eventSource.addEventListener('error', (e) => {
      const data = JSON.parse(e.data);
      setError(data.message);
      eventSource.close();
    });

    eventSource.onerror = (e) => {
      console.error('SSE connection error:', e);
      if (eventSource.readyState === EventSource.CLOSED) {
        setError('Connection lost');
      }
    };

    return () => {
      eventSource.close();
    };
  }, [jobId]);

  return { progress, isComplete, error };
}

// Component usage
function VideoProcessingProgress({ jobId }) {
  const { progress, isComplete, error } = useJobProgress(jobId);

  if (error) {
    return <div className="error">Error: {error}</div>;
  }

  return (
    <div className="progress-container">
      <h3>Processing Status: {progress.stage}</h3>
      <div className="progress-bar">
        <div
          className="progress-fill"
          style={{ width: `${progress.progress}%` }}
        />
      </div>
      <p>{progress.message}</p>
      <span>{progress.progress}%</span>
      {isComplete && <div className="success">✓ Complete!</div>}
    </div>
  );
}
```

## Complete HTML Example

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Video Processing Progress</title>
  <style>
    .progress-container {
      max-width: 600px;
      margin: 50px auto;
      padding: 20px;
      border: 1px solid #ddd;
      border-radius: 8px;
    }
    .progress-bar {
      width: 100%;
      height: 30px;
      background: #f0f0f0;
      border-radius: 15px;
      overflow: hidden;
      margin: 20px 0;
    }
    .progress-fill {
      height: 100%;
      background: linear-gradient(90deg, #4CAF50, #45a049);
      transition: width 0.3s ease;
      display: flex;
      align-items: center;
      justify-content: center;
      color: white;
      font-weight: bold;
    }
    .status { margin: 10px 0; }
    .details {
      background: #f9f9f9;
      padding: 10px;
      border-radius: 4px;
      margin-top: 10px;
      font-family: monospace;
      font-size: 12px;
    }
  </style>
</head>
<body>
  <div class="progress-container">
    <h2>Video Processing Progress</h2>
    <div id="status" class="status">Status: Connecting...</div>
    <div id="stage" class="status">Stage: -</div>
    <div class="progress-bar">
      <div id="progress-fill" class="progress-fill">0%</div>
    </div>
    <div id="message" class="status">-</div>
    <div id="details" class="details" style="display: none;"></div>
  </div>

  <script>
    const jobId = new URLSearchParams(window.location.search).get('jobId');

    if (!jobId) {
      document.getElementById('status').textContent = 'Error: No job ID provided';
    } else {
      const eventSource = new EventSource(`/jobs/${jobId}/stream`);

      eventSource.addEventListener('status', (e) => {
        const data = JSON.parse(e.data);
        updateUI(data);
      });

      eventSource.addEventListener('progress', (e) => {
        const data = JSON.parse(e.data);
        updateUI(data);
      });

      eventSource.addEventListener('complete', (e) => {
        const data = JSON.parse(e.data);
        updateUI(data);
        document.getElementById('status').textContent = '✓ Status: Complete!';
        document.getElementById('status').style.color = 'green';
        eventSource.close();
      });

      eventSource.addEventListener('error', (e) => {
        const data = JSON.parse(e.data);
        document.getElementById('status').textContent = '✗ Status: Failed';
        document.getElementById('status').style.color = 'red';
        document.getElementById('message').textContent = data.message;
        eventSource.close();
      });

      eventSource.onerror = (e) => {
        console.error('Connection error:', e);
      };

      function updateUI(data) {
        document.getElementById('status').textContent = `Status: ${data.status}`;
        document.getElementById('stage').textContent = `Stage: ${data.stage}`;
        document.getElementById('progress-fill').style.width = `${data.progress}%`;
        document.getElementById('progress-fill').textContent = `${data.progress}%`;
        document.getElementById('message').textContent = data.message;

        if (data.details) {
          const detailsDiv = document.getElementById('details');
          detailsDiv.style.display = 'block';
          detailsDiv.textContent = JSON.stringify(data.details, null, 2);
        }
      }
    }
  </script>
</body>
</html>
```

## Nginx Configuration

If you're using Nginx as a reverse proxy, add these directives:

```nginx
location /jobs/ {
    proxy_pass http://your-backend;

    # Critical for SSE
    proxy_buffering off;
    proxy_cache off;
    proxy_set_header Connection '';
    proxy_http_version 1.1;
    chunked_transfer_encoding off;

    # Timeouts (adjust based on your job duration)
    proxy_read_timeout 600s;
    proxy_connect_timeout 600s;
    proxy_send_timeout 600s;
}
```

## Troubleshooting

### Problem: Connection keeps reconnecting

**Solution**:
- Check that `X-Accel-Buffering: no` header is set
- Verify nginx/proxy buffering is disabled
- Ensure heartbeats are being sent every 15 seconds

### Problem: ERR_INCOMPLETE_CHUNKED_ENCODING

**Solution**:
- Ensure `WriteHeader(200)` is called before sending data
- Flush after every write
- Check that proxy/load balancer isn't timing out

### Problem: Events arrive in bursts, not real-time

**Solution**:
- Verify `flusher.Flush()` is called after every write
- Check for proxy buffering
- Ensure channel buffer is large enough (50+)

### Problem: Connection drops during long stages

**Solution**:
- Reduce heartbeat interval (15 seconds recommended)
- Increase proxy/load balancer timeouts
- Check network infrastructure for idle connection limits

## Performance Considerations

- **Channel Buffer**: 50 events is sufficient for most cases. Increase to 100+ for very rapid updates.
- **Heartbeat Interval**: 15 seconds balances connection stability with network overhead.
- **Event Frequency**: Emit updates at meaningful intervals (e.g., per resolution, not per frame).
- **Cleanup**: Always unsubscribe clients when they disconnect to prevent memory leaks.

## Best Practices

1. ✅ Always flush after every write
2. ✅ Send heartbeats regularly (15-20 seconds)
3. ✅ Set proper headers (`X-Accel-Buffering`, `Cache-Control`)
4. ✅ Write status code explicitly before sending data
5. ✅ Handle client disconnections gracefully
6. ✅ Send completion event before closing stream
7. ✅ Use buffered channels (50+) for progress updates
8. ✅ Detect and handle write errors immediately
9. ✅ Implement exponential backoff for reconnections (client-side)
10. ✅ Clean up resources (defer unsubscribe, stop tickers)
