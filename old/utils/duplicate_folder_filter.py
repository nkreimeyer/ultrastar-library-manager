import os
import re
import shutil
from pathlib import Path


def normalize_folder_name(folder_name):
    # Remove all items in square brackets except [DUET]
    return re.sub(r'\[(?!DUET).*?\]', '', folder_name).strip()

def count_brackets(folder_name):
    # Count the number of items in square brackets
    return len(re.findall(r'\[.*?\]', folder_name))


def move_duplicate_folders(pwd):
    # Create the songs.dup directory if it doesn't exist
    dup_dir = Path(pwd).parent.joinpath('songs.dup')
    dup_dir.mkdir(parents=True, exist_ok=True)

    # List all directories in PWD
    all_folders = [f for f in os.listdir(pwd) if os.path.isdir(os.path.join(pwd, f))]
    seen = {}

    for folder in all_folders:
        normalized_name = normalize_folder_name(folder)
        bracket_count = count_brackets(folder)

        if normalized_name in seen:
            # Compare bracket counts and decide which folder to move
            existing_bracket_count, existing_folder = seen[normalized_name]
            if bracket_count < existing_bracket_count:
                # Move the existing folder to songs.dup
                shutil.move(os.path.join(pwd, existing_folder), dup_dir)
                seen[normalized_name] = (bracket_count, folder)
            else:
                # Move the current folder to songs.dup
                shutil.move(os.path.join(pwd, folder), dup_dir)
        else:
            seen[normalized_name] = (bracket_count, folder)
    
def normalize_folders(pwd):
    # List all directories in PWD
    all_folders = [f for f in os.listdir(pwd) if os.path.isdir(os.path.join(pwd, f))]
    for folder in all_folders:
        normalized_name = normalize_folder_name(folder)
        if folder != normalized_name:
            if os.path.exists(os.path.join(pwd, normalized_name)):
                # Folder already exists, move the current folder to songs.dup
                shutil.move(os.path.join(pwd, folder), Path(pwd).parent.joinpath('songs.dup'))
            else:
                os.rename(os.path.join(pwd, folder), os.path.join(pwd, normalized_name))


if __name__ == '__main__':
    import sys
    move_duplicate_folders(sys.argv[1])
    normalize_folders(sys.argv[1])
