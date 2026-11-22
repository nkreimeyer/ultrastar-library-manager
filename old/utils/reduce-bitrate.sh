#!/bin/bash

# Usage: ./reduce-bitrate.sh /path/to/directory
set -e

export TARGET_DIR=$1
export BITRATE_THRESHOLD=3500000 # 3.5 Mbit/s

# Check if the target directory is provided
if [ -z "$TARGET_DIR" ]; then
    echo "Usage: $0 /path/to/directory"
    exit 1
fi

# Function to process video files
process_video_file() {
    local file=$1
    local base_name="${file%.*}"
    local output_file="${base_name}_output.mp4"

    echo "Processing file: $file"

    # Check if the file has an audio stream
    if ffprobe -v error -show_entries stream=codec_type -of default=nokey=1:noprint_wrappers=1 "$file" | grep -q 'audio'; then
        # If audio exists, remove it and overwrite the original file
        ffmpeg -hide_banner -loglevel error -i "$file" -c:v copy -an -y "$output_file" && mv -f "$output_file" "$file"
        echo "Removed audio from: $file"
    fi

    # Check video bitrate
    local bitrate=$(ffprobe -v error -select_streams v:0 -show_entries stream=bit_rate -of csv=p=0 "$file")
    bitrate=${bitrate%.*}

    # Re-encode if the bitrate is too high and overwrite the original file
    if [[ $bitrate -gt $BITRATE_THRESHOLD ]]; then
        echo "Reencode to lower bitrate (was $bitrate bit/s vs $BITRATE_THRESHOLD bits/s): $file"
        ffmpeg -hide_banner -loglevel error -i "$file" -c:v libx264 -b:v 3M -y "$output_file" && mv -f "$output_file" "$file"
    fi
}

export -f process_video_file

# Process video files; find all .mp4 files and call process_video_file on each
find "$TARGET_DIR" -type f -name "*.mp4" -exec bash -c 'process_video_file "$0"' {} \;

echo "All files have been processed."
