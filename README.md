# karaoke-scripts

To build complete statically linked executable for Linux:  

```bash
cd src/main/song-sanitizer
go get
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -tags netgo -ldflags '-w -extldflags "-static"' -o mybin *.go
```

If all went well you nove have a statically linked executable in the current directory called "mybin".

## Features

- Sanitize text files
  - fix encoding issues
    - convert to UTF-8
  - fix MP3 tags
- move unnecessary files to "extra-files" folder one level up
- move broken songs folders to "invalid" folder one level up
  - songs with not enough files
- reencode videos to mp4 with x264 codec when potentially beneficial
  - remove audio track if not needed
  - downscale videos to FullHD if bigger
  - remove extra video tracks
  - reencode old or too new codecs
  - when transcoding fails due to invalid input data, flag the song as potentially corrupted in a logfile

## Usage

Just call the binary. It outputs its usage information then.
