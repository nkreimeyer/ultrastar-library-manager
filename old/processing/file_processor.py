import os
from typing import Optional

from thefuzz import fuzz

from song_models import SongDirectory


def read_file(directory: str, file: str):
    """ read file and try to sanitize it if it can't be decoded """

    target = f"{directory}/{file}"
    try:
        with open(target, "r") as f:
            return f.read()

    # some files have weird encodings. like the 'fancy quote'
    # https://stackoverflow.com/questions/46000191/utf-8-codec-cant-decode-byte-0x92-in-position-18-invalid-start-byte

    # also many files have other things like the EOL terminator '0x0d' instead of \n
    # so let's just try to open them and then rewrite as utf-8
    except UnicodeDecodeError:
        # encoding that stack overflow recommended
        with open(target, "r", encoding='cp1252') as f:
            text = f.read()
            text = text.replace("’", "'")

        with open(target, "w") as f:
            f.write(text)

        print(f"fixed 'fancy quote' in '{target}'")

        return text


def get_songs(start_path="./songs") -> list[SongDirectory]:
    """
    Iterate over all song folders, look at the files inside and represent them as a SongDirectory
    License.txt files will be ignored / filtered
    """
    print("generating index for", start_path)
    # Sorting index

    paths = os.listdir(start_path)
    paths.sort()
    song_dirs = []

    for folder_name in paths:
        sub_dir = f"{start_path}/{folder_name}"
        sub_files = os.listdir(sub_dir)
        has_license = False

        # we ignore license files
        if "License.txt" in sub_files:
            sub_files.remove("License.txt")
            has_license = True

        song_dirs.append(
            SongDirectory(name=folder_name, path=sub_dir, files_in_path=sub_files, has_license=has_license)
        )

    return song_dirs


def compare_two_songs(directory: str, files: list[str]) -> Optional[int]:
    """
    Compare two versions of the 'same' song and see how good they match
    :returns: the ration that the files match
    """
    if len(files) != 2:
        print(f"Can't process {directory=}, {files=}, too many files")
        return None

    # read in files
    texts: list[str] = []
    for song in files:
        text = read_file(directory, song)
        texts.append(text)

    # print(texts)

    ratio = fuzz.ratio(texts[0], texts[1])

    print(f"[{ratio: >3}]% match for: {directory}")

    return ratio


def check_for_more_than_one_song_file(songs: list[SongDirectory]):
    """ iterate over list of all SongDirectories and looks at Dirs with more than one file """
    for song_dir in songs:
        if len(song_dir.files_in_path) == 1:
            continue

        compare_two_songs(song_dir.path, song_dir.files_in_path)


if __name__ == '__main__':
    _songs = get_songs()
    # print(_songs)
    check_for_more_than_one_song_file(_songs)
