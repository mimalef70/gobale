# Synthetic media fixtures

These files contain no captured account or user data. Build-time FFmpeg generated
both fixtures; FFmpeg is not a gateway runtime dependency.

- `preview.mp4`: one second, 160×90 solid blue, 12 fps, AVC/H.264 in ordinary MP4.
- `audio.mp3`: two-second 440 Hz sine, mono MPEG layer III, 64 kbit/s.

Generation:

```sh
ffmpeg -f lavfi -i color=c=blue:s=160x90:r=12:d=1 -c:v libx264 -pix_fmt yuv420p preview.mp4
ffmpeg -f lavfi -i sine=frequency=440:duration=2 -ac 1 -codec:a libmp3lame -b:a 64k audio.mp3
```

Tests inspect real container/frame timing, decode the actual blue IDR pixels,
and reject truncated streams and sample offsets outside media data.
