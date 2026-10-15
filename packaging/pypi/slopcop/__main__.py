"""Run the SlopCop binary that ships inside this wheel.

Every wheel carries the binary built for its platform, so the PyPI install
behaves exactly like the release archives: same output, same exit codes. This
module only finds that binary and hands the process over to it, which is also
what makes ``python -m slopcop`` work.
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

#: The binary name inside the wheel, as build_wheels.py packs it.
_BINARY = "slopcop.exe" if os.name == "nt" else "slopcop"


def binary_path() -> Path:
    """The bundled binary for this platform."""
    return Path(__file__).resolve().parent / "_bin" / _BINARY


def main() -> int:
    binary = binary_path()
    if not binary.is_file():
        print(
            f"slopcop: {binary} is missing; this wheel does not match this platform",
            file=sys.stderr,
        )
        # The CLI's exit code for a run that could not happen.
        return 2

    argv = [str(binary), *sys.argv[1:]]
    if os.name == "nt":
        # os.execv does not hand the process over on Windows, so run the binary
        # as a child and pass its exit code on unchanged.
        return subprocess.call(argv)

    # Otherwise the binary owns the process, so signals and the exit code are
    # the same as running it directly.
    if not os.access(binary, os.X_OK):
        # pip keeps the executable bit from the wheel; another installer may
        # not, and a non-executable file cannot be run at all.
        try:
            binary.chmod(binary.stat().st_mode | 0o111)
        except OSError:
            pass
    sys.stdout.flush()
    sys.stderr.flush()
    os.execv(str(binary), argv)
    return 2  # not reached: execv replaces this process


if __name__ == "__main__":
    # `python -m slopcop`: the console script calls main() itself.
    raise SystemExit(main())
