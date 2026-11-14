#!/usr/bin/env python3
# Copyright (C) 2026 Lukas Koch and Kyro contributors
# SPDX-License-Identifier: GPL-3.0-or-later
"""Records the screen of a QEMU machine in real time and makes a video of it.

    record.py capture <monitor socket> <directory>
    record.py video <directory> <output.mp4> [caption]

"capture" asks the machine's monitor for a picture several times a second
until the machine is gone. Every picture is kept with the time at which it
was taken, so that the video plays at the speed at which things happened.
A picture that equals the one before it is not stored again.

"video" turns that into a video with a clock in the corner. Nothing is
skipped and nothing is sped up.

Needs Python with Pillow, and ffmpeg for "video".
"""

import hashlib
import os
import socket
import subprocess
import sys
import time

from PIL import Image

WIDTH, HEIGHT = 1280, 800
INTERVAL = 0.25  # seconds between pictures
FPS = 10  # of the video
PROMPT = b"(qemu) "


def read_prompt(sock):
    """Reads until the monitor is ready for the next command."""
    data = b""
    while not data.endswith(PROMPT):
        chunk = sock.recv(4096)
        if not chunk:
            raise ConnectionError("the monitor closed")
        data += chunk
    return data


def capture(sock_path, directory):
    os.makedirs(directory, exist_ok=True)
    dump = os.path.join(directory, "current.ppm")
    index = open(os.path.join(directory, "frames.txt"), "w", buffering=1)
    sock = socket.socket(socket.AF_UNIX)
    sock.connect(sock_path)
    read_prompt(sock)
    start = time.monotonic()
    last, count = None, 0
    try:
        while True:
            began = time.monotonic()
            sock.sendall(b"screendump " + dump.encode() + b"\n")
            read_prompt(sock)
            taken = time.monotonic() - start
            try:
                with open(dump, "rb") as f:
                    raw = f.read()
                digest = hashlib.sha1(raw).digest()
                if digest != last:
                    image = Image.open(dump).convert("RGB")
                    if image.size != (WIDTH, HEIGHT):
                        # Keep the shape and centre it on black.
                        image.thumbnail((WIDTH, HEIGHT))
                        canvas = Image.new("RGB", (WIDTH, HEIGHT))
                        canvas.paste(image, ((WIDTH - image.width) // 2, (HEIGHT - image.height) // 2))
                        image = canvas
                    count += 1
                    name = "frame-%06d.jpg" % count
                    image.save(os.path.join(directory, name), quality=88)
                    index.write("%.3f %s\n" % (taken, name))
                    last = digest
            except (OSError, SyntaxError):
                pass  # a picture that was not complete; the next one comes soon
            time.sleep(max(0.0, INTERVAL - (time.monotonic() - began)))
    except (ConnectionError, BrokenPipeError, OSError):
        pass
    finally:
        index.write("%.3f end\n" % (time.monotonic() - start))
        index.close()
        if os.path.exists(dump):
            os.remove(dump)


def video(directory, output, caption=""):
    entries = []
    end = None
    with open(os.path.join(directory, "frames.txt")) as f:
        for line in f:
            at, name = line.split()
            if name == "end":
                end = float(at)
            else:
                entries.append((float(at), name))
    if not entries:
        sys.exit("no pictures in " + directory)
    if end is None:
        end = entries[-1][0] + 1
    # ffmpeg's list format: every picture with how long it stays.
    listing = os.path.join(directory, "frames.ffconcat")
    with open(listing, "w") as f:
        f.write("ffconcat version 1.0\n")
        for i, (at, name) in enumerate(entries):
            until = entries[i + 1][0] if i + 1 < len(entries) else end
            f.write("file '%s'\nduration %.3f\n" % (name, max(until - at, 0.001)))
    font = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
    clock = (
        "drawtext=fontfile=%s:text='%%{pts\\:hms}':x=w-tw-16:y=14:fontsize=26:"
        "fontcolor=white:box=1:boxcolor=black@0.65:boxborderw=8" % font
    )
    filters = ["fps=%d" % FPS, clock]
    if caption:
        safe = caption.replace("\\", "").replace("'", "").replace(":", "\\:").replace("%", "")
        filters.append(
            "drawtext=fontfile=%s:text='%s':x=16:y=14:fontsize=20:"
            "fontcolor=white:box=1:boxcolor=black@0.65:boxborderw=8" % (font, safe)
        )
    filters.append("format=yuv420p")
    subprocess.run(
        [
            "ffmpeg", "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", listing,
            "-vf", ",".join(filters), "-c:v", "libx264", "-preset", "slow", "-crf", "28",
            "-g", str(FPS * 10), "-movflags", "+faststart", output,
        ],
        check=True,
    )


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "capture":
        capture(sys.argv[2], sys.argv[3])
    elif len(sys.argv) in (4, 5) and sys.argv[1] == "video":
        video(*sys.argv[2:])
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
