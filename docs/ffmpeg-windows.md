# Installing ffmpeg on Windows

`library-manager` needs `ffmpeg` and `ffprobe` (version 5.1 or newer) for the video and audio
tools and for the media checks. Both come in the same download. Without them everything that
only reads the song texts still works.

To see whether it already works, open **PowerShell** (Start menu, type `powershell`) and run:

```powershell
ffmpeg -version
ffprobe -version
```

If both print a version number of 5.1 or higher, you are done. If Windows says the command is
not found, pick one of the ways below.

After installing, **close and reopen library-manager** (and any open PowerShell window): a
program only sees the new `PATH` after it was started again.

## Way 1: one command with winget (recommended, sets up PATH for you)

`winget` is Microsoft's own package manager, part of Windows 10 (version 1809 and later) and
Windows 11. In PowerShell run:

```powershell
winget install ffmpeg
```

Answer `Y` if it asks to accept the terms. This installs ffmpeg **and makes `ffmpeg` and
`ffprobe` available from any terminal automatically**: no manual `PATH` editing. (The package
registers command aliases for `ffmpeg`, `ffprobe` and `ffplay` in winget's own links folder,
which is already on your `PATH`.) Close and reopen library-manager and any open PowerShell
window afterwards, then check with `ffmpeg -version`. Update later with `winget upgrade ffmpeg`.

Is this a trustworthy source? Yes, as far as these things go:

- The package is published in Microsoft's community repository
  ([microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs/tree/master/manifests/g/Gyan/FFmpeg)),
  which runs automated checks on submissions, and winget verifies the download's hash.
- It installs the "release full" build from [gyan.dev](https://www.gyan.dev/ffmpeg/builds/),
  which is one of the two Windows builders that the official FFmpeg site links to
  ([ffmpeg.org/download.html](https://ffmpeg.org/download.html), "Windows builds from
  gyan.dev"). FFmpeg itself publishes only source code, no binaries, so a third-party build
  is how everyone gets it on Windows.
- The package's download comes from the builder's GitHub project
  ([GyanD/codexffmpeg](https://github.com/GyanD/codexffmpeg/releases)).

Still, it is a build by one person and not by the FFmpeg project. If you want less
dependence on that, follow way 2 with the zip of the other builder that ffmpeg.org links,
[BtbN](https://github.com/BtbN/FFmpeg-Builds/releases), or build it yourself.

If `winget` is not found, use way 2.

## Way 2: no command line, just a download (you set up PATH yourself)

This is the way for everyone who does not want to type commands. It takes about five minutes.

1. Open <https://www.gyan.dev/ffmpeg/builds/> (linked from ffmpeg.org) and download
   **ffmpeg-release-essentials.zip** (under "release builds"). Any build that contains `ffmpeg.exe` and `ffprobe.exe` works.
2. Right-click the downloaded zip and choose **Extract All…**. The result is a folder like
   `ffmpeg-7.1-essentials_build` with a `bin` folder inside. It holds `ffmpeg.exe`,
   `ffprobe.exe` and `ffplay.exe`.
3. Move that folder to a place that stays, for example `C:\ffmpeg`, so that
   `C:\ffmpeg\bin\ffmpeg.exe` exists. (Do not leave it in Downloads: if you delete it later,
   library-manager loses ffmpeg.)
4. Add the `bin` folder to `PATH`, which is how Windows finds programs:
   1. Press the Windows key, type `environment variables` and open **Edit environment
      variables for your account**.
   2. In the upper list ("User variables") select **Path** and click **Edit…**.
   3. Click **New**, type `C:\ffmpeg\bin` and press Enter.
   4. Click **OK** in all windows. No administrator rights are needed.
5. Restart library-manager. In a newly opened PowerShell, `ffmpeg -version` should now work.

Copying `ffmpeg.exe` next to `library-manager.exe` does **not** work: the program only looks
in the folders of `PATH`.

## Way 3: a package manager

If you already use one:

```powershell
choco install ffmpeg      # Chocolatey
scoop install ffmpeg      # Scoop
```

## Problems

- **"ffmpeg was not found on PATH" in library-manager, but PowerShell finds it:**
  library-manager was started before the installation. Close it and start it again.
- **The version is older than 5.1:** install a current build (way 1 or 2). If an old
  ffmpeg from another program comes first on `PATH`, remove it or put `C:\ffmpeg\bin`
  above it in the `Path` list.
- **Windows SmartScreen warns about `library-manager.exe`:** the program is not signed.
  Click **More info**, then **Run anyway**.
