# StreamForge

StreamForge is a powerful, production-ready video processing pipeline built in Go that handles video ingestion, transcoding, quality validation (VMAF), packaging, and storage. It leverages Temporal for workflow orchestration, ensuring reliability, scalability, and fault tolerance.

## Features

- **Video Transcoding**: Multi-resolution transcoding with configurable codecs (libx264, libx265, libaom-av1) and bitrates
- **Quality Validation**: VMAF (Video Multi-Method Assessment Fusion) scoring to ensure output quality meets thresholds
- **Streaming Package Generation**: Create HLS and DASH packages for adaptive bitrate streaming
- **Storage Backends**: Support for local storage and AWS S3 (with extensible architecture for GCloud and R2)
- **Workflow Orchestration**: Built on Temporal for reliable, scalable, and fault-tolerant video processing
- **REST API**: HTTP API for submitting video processing jobs
- **Retry Logic**: Configurable exponential backoff retry mechanism for all operations
- **Concurrent Processing**: Parallel processing with configurable worker pools
- **Comprehensive Logging**: Structured logging with zap logger

## Architecture

StreamForge follows a modular architecture with clear separation of concerns:

```
┌─────────────┐
│  REST API   │
│   (Port     │
│   8081)     │
└──────┬──────┘
       │
       ▼
┌─────────────────┐
│ Temporal Client │
└──────┬──────────┘
       │
       ▼
┌─────────────────────────────────────┐
│   Temporal Workflow Orchestration   │
│  ┌──────────┐  ┌──────────┐         │
│  │ Ingest   │→ │ Transcode│         │
│  └──────────┘  └────┬─────┘         │
│                     │               │
│                ┌────▼─────┐         │
│                │  VMAF    │         │
│                │ Validate │         │
│                └────┬─────┘         │
│                     │               │
│                ┌────▼─────┐         │
│                │ Package  │         │
│                └────┬─────┘         │
│                     │               │
│                ┌────▼─────┐         │
│                │  Store   │         │
│                └──────────┘         │
└─────────────────────────────────────┘
```

## Prerequisites

- **Go 1.25.0** or later
- **FFmpeg** installed and available in PATH (or specify path in config)
- **Temporal Server** running (default: `localhost:7233`)
  - You can use Docker Compose to run Temporal locally:
    ```bash
    docker-compose up -d
    ```
    (See the Temporal documentation for setup instructions)

## Installation

1. Clone the repository:
```bash
git clone <repository-url>
cd StreamForge
```

2. Install dependencies:
```bash
go mod download
```

3. Build the project:
```bash
go build -o streamforge ./cmd/streamforge
```

## Configuration

StreamForge uses a YAML configuration file (`config.yaml`) for all settings. Here's an example configuration:

```yaml
pipeline:
  max_workers: 4
  ff_mpeg_path: "ffmpeg"  # or path to your ffmpeg binary
  retry:
    max_attempts: 3
    initial_interval_sec: 1.0
    backoff_coefficient: 2.0
  vmaf:
    enabled: true
    min_score: 80.0

storage:
  type: "local"  # options: local, s3
  # For S3 storage, uncomment and configure:
  # s3:
  #   bucket: "your-bucket-name"
  #   region: "us-east-1"
  #   access_key_id: "your-access-key"
  #   secret_access_key: "your-secret-key"

transcode:
  - codec: "libx264"
    bitrate: "1000k"
    resolution: "720"
  - codec: "libx264"
    bitrate: "500k"
    resolution: "480"

package:
  - format: "hls"
    segment_duration: 10
    llhls: false
    output_path: "playlist.m3u8"

plugins: []

logging:
  level: "info"
  output: "console"
```

### Configuration Options

#### Pipeline
- `max_workers`: Maximum number of concurrent workers for transcoding/packaging
- `ff_mpeg_path`: Path to FFmpeg binary (default: "ffmpeg")
- `retry`: Retry configuration for all operations
- `vmaf`: VMAF quality validation settings

#### Storage
- `type`: Storage backend type (`local`, `s3`)
- For S3: Configure `bucket`, `region`, `access_key_id`, and `secret_access_key`

#### Transcode
- `codec`: Video codec (`libx264`, `libx265`, `libaom-av1`)
- `bitrate`: Target bitrate (e.g., "1000k", "2M")
- `resolution`: Output resolution height (e.g., "720", "1080")

#### Package
- `format`: Package format (`hls`, `dash`)
- `segment_duration`: Segment duration in seconds
- `llhls`: Enable Low-Latency HLS (for HLS format)
- `output_path`: Output playlist filename

## Usage

### Starting the Server

1. Ensure Temporal server is running:
```bash
# Using Docker Compose (if you have a docker-compose.yml)
docker-compose up -d
```

2. Start the StreamForge server:
```bash
./streamforge
# or
go run ./cmd/streamforge
```

The server will start on port `8081` and connect to Temporal at `localhost:7233`.

### API Endpoints

#### Health Check
```bash
GET /health
```

Response:
```json
{
  "status": "healthy",
  "service": "streamforge",
  "version": "1.0.0"
}
```

#### Process Video
```bash
POST /process
Content-Type: multipart/form-data

Form data:
- video: (file) - The video file to process
- key: (optional) - Custom filename (default: "video.mp4")
```

Example using curl:
```bash
curl -X POST http://localhost:8081/process \
  -F "video=@input/video.mp4" \
  -F "key=my-video.mp4"
```

Response:
```json
{
  "result": {
    "ingest_result": {...},
    "transcode_results": [...],
    "vmaf_results": [...],
    "package_results": [...],
    "storage_results": [...],
    "duration": "5m30s"
  }
}
```

### Using the SDK

StreamForge provides an SDK for programmatic access:

```go
import (
    "StreamForge/internal/sdk"
    "context"
    "os"
)

// Initialize SDK client
client := sdk.NewClient(...)

// Process a video
file, _ := os.Open("video.mp4")
result, err := client.RunWorkflow(ctx, file, "input", "video.mp4")
```

### Command Line Tools

#### SDK Client
```bash
go run ./cmd/sdk
```

#### Temporal Worker (Standalone)
```bash
go run ./cmd/temporal
```

## Project Structure

```
StreamForge/
├── cmd/
│   ├── sdk/              # SDK client example
│   ├── streamforge/      # Main server application
│   └── temporal/         # Temporal worker example
├── internal/
│   ├── api/              # REST API server
│   ├── config/           # Configuration loading and validation
│   ├── models/           # VMAF model files
│   ├── pipeline/         # Core pipeline components
│   │   ├── ingest.go     # Video ingestion
│   │   ├── transcode.go  # Video transcoding
│   │   ├── monitor.go    # VMAF quality validation
│   │   ├── package.go    # HLS/DASH packaging
│   │   ├── workflow.go   # Traditional workflow (non-Temporal)
│   │   ├── temporal.go   # Temporal workflow orchestration
│   │   ├── retry.go      # Retry logic
│   │   └── storage/      # Storage backends
│   │       ├── storage.go    # Storage interface
│   │       ├── factory.go    # Storage factory
│   │       ├── local.go      # Local storage implementation
│   │       └── s3.go         # S3 storage implementation
│   └── sdk/              # SDK client library
├── pkg/
│   ├── types.go          # Shared type definitions
│   └── ffmpeg/           # FFmpeg wrapper
├── input/                # Input video directory
├── outputs/              # Output directory (organized by epoch time)
├── config.yaml           # Configuration file
├── go.mod                # Go module dependencies
└── main.go               # Example main file
```

## Components

### Transcoder
Handles video transcoding to multiple resolutions and codecs. Supports parallel processing with configurable worker pools.

### Monitor
Performs VMAF quality validation to ensure transcoded videos meet quality thresholds. Uses FFmpeg's libvmaf filter.

### Packager
Creates streaming packages (HLS/DASH) from transcoded videos. Supports standard HLS and Low-Latency HLS (LL-HLS).

### Storage
Abstract storage interface with implementations for:
- **Local Storage**: Files stored in local filesystem
- **S3 Storage**: AWS S3 integration
- **Extensible**: Architecture supports GCloud and R2 (not yet implemented)

### Temporal Workflow
Orchestrates the entire video processing pipeline:
1. **Ingest**: Save uploaded file locally
2. **Transcode**: Convert to multiple resolutions/codecs
3. **VMAF Validation**: Validate quality (parallel)
4. **Package**: Create HLS/DASH packages
5. **Store**: Upload to configured storage backend

## Output Structure

Processed videos are organized by epoch timestamp:

```
outputs/
└── {epoch_time}/
    ├── 720/
    │   ├── video.mp4
    │   ├── vmaf.json
    │   └── package/
    │       ├── playlist.m3u8
    │       ├── playlist0.ts
    │       ├── playlist1.ts
    │       └── ...
    └── 480/
        ├── video.mp4
        ├── vmaf.json
        └── package/
            ├── playlist.m3u8
            └── ...
```

## Development

### Running Tests
```bash
go test ./...
```

### Building
```bash
# Build all binaries
go build -o bin/streamforge ./cmd/streamforge
go build -o bin/sdk ./cmd/sdk
go build -o bin/temporal ./cmd/temporal
```

### Code Structure
- **Internal packages**: Application-specific code (not for external use)
- **Pkg packages**: Reusable types and utilities
- **Cmd packages**: Application entry points

## Dependencies

### Core Dependencies
- **go.temporal.io/sdk**: Temporal workflow orchestration
- **go.uber.org/zap**: Structured logging
- **github.com/spf13/viper**: Configuration management
- **github.com/aws/aws-sdk-go-v2**: AWS S3 integration

### External Tools
- **FFmpeg**: Video processing (must be installed separately)

## Troubleshooting

### FFmpeg Not Found
Ensure FFmpeg is installed and available in PATH, or specify the full path in `config.yaml`:
```yaml
pipeline:
  ff_mpeg_path: "/usr/local/bin/ffmpeg"
```

### Temporal Connection Issues
Verify Temporal server is running:
```bash
# Check if Temporal is accessible
curl http://localhost:7233
```

### VMAF Validation Fails
- Ensure FFmpeg is compiled with libvmaf support
- Check that the VMAF model file exists at `internal/models/vmaf_v0.6.1.json`
- Verify input and output files are valid video files

### Storage Upload Failures
- For S3: Verify credentials and bucket permissions
- Check network connectivity
- Review retry configuration if uploads are timing out

