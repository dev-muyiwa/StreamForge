# HLS Master Playlist Structure

## Overview
The system now automatically generates a master HLS playlist (`master.m3u8`) that links all resolution variants, enabling adaptive bitrate streaming.

## File Structure

```
outputs/
└── {epochTime}/
    ├── master.m3u8              ← Master playlist (adaptive streaming)
    ├── 720/
    │   └── package/
    │       ├── playlist.m3u8    ← 720p variant playlist
    │       ├── segment000.ts
    │       ├── segment001.ts
    │       └── ...
    └── 480/
        └── package/
            ├── playlist.m3u8    ← 480p variant playlist
            ├── segment000.ts
            ├── segment001.ts
            └── ...
```

## Master Playlist Content Example

```m3u8
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720
720/package/playlist.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480
480/package/playlist.m3u8
```

## Stream URL Format

When you query `/jobs/:id`, the `stream_url` field will return:

```
/outputs/{epochTime}/master.m3u8
```

Example:
```json
{
  "id": "123e4567-e89b-12d3-a456-426614174000",
  "status": "completed",
  "progress": 100,
  "resolutions": ["720p", "480p"],
  "peak_vmaf_score": 92.45,
  "stream_url": "/outputs/1735689600/master.m3u8",
  ...
}
```

## Benefits

1. **Adaptive Bitrate Streaming**: Video players automatically select the best quality based on network conditions
2. **Single URL**: Users only need the master playlist URL to stream all resolutions
3. **Standard Compliance**: Follows HLS specification for multi-variant playlists
4. **Player Compatibility**: Works with all HLS-compatible players (video.js, hls.js, native iOS/Safari, etc.)

## How It Works

1. **Transcoding**: Videos are transcoded to multiple resolutions (e.g., 720p, 480p)
2. **Packaging**: Each resolution gets its own HLS playlist and segments
3. **Master Playlist**: A master playlist is created that references all resolution variants
4. **Metadata**: The master playlist path is stored in the job's `stream_url` field

## Player Integration Example

### Using hls.js (JavaScript)

```html
<video id="video" controls></video>
<script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
<script>
  const video = document.getElementById('video');
  const streamUrl = '/outputs/1735689600/master.m3u8';

  if (Hls.isSupported()) {
    const hls = new Hls();
    hls.loadSource(streamUrl);
    hls.attachMedia(video);
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    // Native HLS support (Safari, iOS)
    video.src = streamUrl;
  }
</script>
```

### Using Video.js

```html
<video id="video" class="video-js" controls preload="auto">
  <source src="/outputs/1735689600/master.m3u8" type="application/x-mpegURL">
</video>
<script src="https://vjs.zencdn.net/7.20.3/video.min.js"></script>
<script>
  const player = videojs('video');
</script>
```

## Bandwidth Estimation

The system estimates bandwidth for each resolution variant:

| Resolution | Bandwidth | Typical Use Case |
|-----------|-----------|------------------|
| 2160p (4K) | 20 Mbps | Premium quality, high-speed connections |
| 1440p (2K) | 10 Mbps | High quality, good connections |
| 1080p (Full HD) | 5 Mbps | Standard HD streaming |
| 720p (HD) | 2.5 Mbps | HD on moderate connections |
| 480p (SD) | 1 Mbps | Standard definition, mobile networks |
| 360p | 600 Kbps | Low bandwidth, older devices |

Note: These are estimates. Actual bandwidth may vary based on content complexity and encoding settings.
