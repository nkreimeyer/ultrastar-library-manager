#!/bin/bash
# Deletes (rm -rf) every top-level folder of -d that contains no *.<-f> file; -n = dry run.

# Usage information
function usage() {
    echo "Usage: $0 -d DIRECTORY -f FILETYPE [-n]"
    echo "  -d DIRECTORY    Specify the directory to scan."
    echo "  -f FILETYPE     Specify the file extension to keep directories for."
    echo "  -n              Dry run. Show what would be deleted without actually deleting."
    exit 1
}

# Parse command-line options
while getopts "d:f:n" opt; do
    case ${opt} in
        d ) directory=${OPTARG} ;;
        f ) filetype=${OPTARG} ;;
        n ) dryrun=1 ;;
        * ) usage ;;
    esac
done

# Check for mandatory options
if [ -z "${directory}" ] || [ -z "${filetype}" ]; then
    echo "Both -d and -f options are mandatory."
    usage
fi

# Check if the specified directory exists
if [ ! -d "${directory}" ]; then
    echo "Directory does not exist: ${directory}"
    exit 2
fi

# Function to either delete or display directories
function process_directory() {
    local dir=$1
    if [ -z "${dryrun}" ]; then
        echo "Deleting: ${dir}"
        rm -rf "${dir}"
    else
        echo "Dry run - would delete: ${dir}"
    fi
}

# Main logic to scan directories and delete if no specified file type is found
find "${directory}" -mindepth 1 -maxdepth 1 -type d | while read -r dir; do
    if [ -z "$(find "${dir}" -type f -name "*.${filetype}")" ]; then
        process_directory "${dir}"
    fi
done

