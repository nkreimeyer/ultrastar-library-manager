import os
import argparse

def read_file_with_encoding(file_path, encodings=['utf-8', 'ISO-8859-1', 'cp1252']):
    """Try reading a file with different encodings until successful."""
    for encoding in encodings:
        try:
            with open(file_path, 'r', encoding=encoding) as file:
                return file.readlines()
        except UnicodeDecodeError:
            continue
    raise UnicodeDecodeError(f"Unable to read file {file_path} with provided encodings.")

def write_file_as_utf8(file_path, lines):
    """Write the file with UTF-8 encoding."""
    with open(file_path, 'w', encoding='utf-8') as file:
        file.writelines(lines)

def update_mp3_filenames(root_dir):
    for subdir, _, files in os.walk(root_dir):
        mp3_file = None
        txt_file = None

        # Identify the .mp3 and .txt files
        for file in files:
            if file.endswith('.mp3'):
                mp3_file = file
            elif file.endswith('.txt'):
                txt_file = os.path.join(subdir, file)

        if mp3_file and txt_file:
            # Try reading the txt file using different possible encodings
            try:
                lines = read_file_with_encoding(txt_file)
            except UnicodeDecodeError:
                print(f"Error reading {txt_file}. Unable to determine file encoding.")
                continue

            modified = False
            updated_lines = []
            for line in lines:
                # If the line contains the #MP3 tag, check for filename mismatch
                if line.startswith('#MP3:'):
                    current_mp3 = line.split(':', 1)[1].strip()
                    if current_mp3 != mp3_file:
                        print(f"Updating {txt_file}: {current_mp3} to {mp3_file}")
                        line = f"#MP3:{mp3_file}\n"
                        modified = True
                updated_lines.append(line)

            if modified:
                # Write the updated list of lines back to the file using UTF-8 encoding
                write_file_as_utf8(txt_file, updated_lines)

def main():
    parser = argparse.ArgumentParser(description="Update MP3 filenames in Ultrastar song files.")
    parser.add_argument("root_directory", help="Path to the root directory of Ultrastar songs")
    args = parser.parse_args()
    update_mp3_filenames(args.root_directory)

if __name__ == "__main__":
    main()