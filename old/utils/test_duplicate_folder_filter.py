import pytest
import os
import shutil
from duplicate_folder_filter import normalize_folder_name, count_brackets, move_duplicate_folders
from pathlib import Path


def setup_module(module):
    # Create a test directory structure
    os.makedirs('test_dir/A [DUET]', exist_ok=True)
    os.makedirs('test_dir/A [DUET] [LIVE]', exist_ok=True)
    os.makedirs('test_dir/B', exist_ok=True)
    os.makedirs('test_dir/B [COVER]', exist_ok=True)
    os.makedirs('test_dir/C [DUET]', exist_ok=True)
    os.makedirs('test_dir/D', exist_ok=True)


def teardown_module(module):
    # Remove the test directory structure
    shutil.rmtree('test_dir')
    shutil.rmtree(Path('test_dir').parent.joinpath('songs.dup'))


def test_normalize_folder_name():
    assert normalize_folder_name('A [DUET] [LIVE]') == 'A [DUET]'
    assert normalize_folder_name('B [COVER]') == 'B'
    assert normalize_folder_name('C [DUET]') == 'C [DUET]'


def test_count_brackets():
    assert count_brackets('A [DUET] [LIVE]') == 2
    assert count_brackets('B [COVER]') == 1
    assert count_brackets('C [DUET]') == 1


def test_move_duplicate_folders():
    move_duplicate_folders('test_dir')
    assert not os.path.exists('test_dir/A [DUET] [LIVE]')
    assert os.path.exists('test_dir/A [DUET]')
    assert not os.path.exists('test_dir/B [COVER]')
    assert os.path.exists('test_dir/B')
    assert os.path.exists('test_dir/C [DUET]')
    assert os.path.exists('test_dir/D')
    assert os.path.exists('test_dir/../songs.dup/A [DUET] [LIVE]')
    assert os.path.exists('test_dir/../songs.dup/B [COVER]')