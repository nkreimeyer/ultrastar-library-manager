from dataclasses import dataclass


@dataclass(frozen=True)
class SongFile:
    title: str
    author: str


@dataclass(frozen=True)
class SongDirectory:
    name: str
    path: str
    files_in_path: list[str]
    has_license: bool
