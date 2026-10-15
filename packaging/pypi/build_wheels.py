#!/usr/bin/env python3
"""Build the PyPI wheels for one SlopCop release.

Releases are tag-driven. GoReleaser builds the platform binaries and publishes
the GitHub release, but it has no PyPI publisher (its Python builder is not
shipped yet), so this script turns the binaries GoReleaser just built into one
wheel per platform, and the release workflow uploads them with the PyPA
action.

Each wheel carries the real binary for its platform plus the launcher package
in ``packaging/pypi/slopcop``, so ``pip install slopcop``, ``pipx install
slopcop``, and ``uvx slopcop`` all run the same binary as the archives.

A wheel is a zip with a ``<name>-<version>.dist-info`` directory, and writing
it here keeps the build dependency-free: no packaging backend has to be
installed on the runner, and the metadata is the file that ships.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import stat
import struct
import sys
import time
import zipfile
from pathlib import Path

#: The Go binary GoReleaser builds (.goreleaser.yaml), and the command the
#: wheel installs.
BINARY = "slopcop"

#: The distribution name on PyPI. It cannot be `slopcop`, which an unrelated
#: project already publishes, but it still installs the `slopcop` command.
DIST_NAME = "slopcop-cli"

#: The import package inside the wheel. It follows the distribution name
#: because the `slopcop` module belongs to that other project, and two
#: distributions cannot install the same module without breaking each other.
PACKAGE = "slopcop_cli"

SUMMARY = "Check a change through the hosted SlopCop check API"

CLASSIFIERS = (
    "Environment :: Console",
    "Intended Audience :: Developers",
    "Operating System :: MacOS",
    "Operating System :: Microsoft :: Windows",
    "Operating System :: POSIX :: Linux",
    "Topic :: Software Development :: Quality Assurance",
)

PROJECT_URLS = (
    ("Homepage", "https://github.com/BrokkAi/slopcop"),
    ("Repository", "https://github.com/BrokkAi/slopcop"),
    ("Issues", "https://github.com/BrokkAi/slopcop/issues"),
)

ENTRY_POINTS = f"[console_scripts]\n{BINARY} = {PACKAGE}.__main__:main\n"

#: Zip members cannot carry a timestamp before 1980, and nothing needs the
#: real one, so wheels are stamped from SOURCE_DATE_EPOCH when a release sets
#: it and from this otherwise.
ZIP_EPOCH = 315532800

LINUX_ARCH = {"amd64": "x86_64", "arm64": "aarch64"}
DARWIN_ARCH = {"amd64": "x86_64", "arm64": "arm64"}
WINDOWS_ARCH = {"amd64": "amd64", "arm64": "arm64"}

MH_MAGIC_64 = 0xFEEDFACF
MH_MAGIC = 0xFEEDFACE
LC_VERSION_MIN_MACOSX = 0x24
LC_BUILD_VERSION = 0x32

SIMPLE_VERSION = re.compile(r"^\d+(\.\d+)*$")
PRERELEASE_VERSION = re.compile(r"^(\d+(?:\.\d+)*)[-_.]?([a-zA-Z]+)\.?(\d+)?$")
PRERELEASE_WORDS = {
    "a": "a",
    "alpha": "a",
    "b": "b",
    "beta": "b",
    "c": "rc",
    "rc": "rc",
    "pre": "rc",
    "preview": "rc",
    "dev": "dev",
}


class BuildError(Exception):
    """A release cannot be packaged as it stands."""


def export_name(name: str) -> str:
    """The name as it appears in a wheel filename and a dist-info directory."""
    return re.sub(r"[-_.]+", "_", name).lower()


def pypi_version(tag: str) -> str:
    """The PEP 440 version for a release tag.

    Tags look like ``v1.2.3`` (.goreleaser.yaml), and PyPI cannot carry the
    leading ``v`` or the ``-rc1`` spelling, so normalize the tag here and fail
    the build on anything that has no PEP 440 spelling.
    """
    version = tag[1:] if tag.startswith("v") else tag
    if SIMPLE_VERSION.match(version):
        return version
    match = PRERELEASE_VERSION.match(version)
    if match:
        base, word, number = match.groups()
        kind = PRERELEASE_WORDS.get(word.lower())
        if kind:
            return f"{base}{kind}{number or '0'}"
    raise BuildError(f"tag {tag!r} has no PEP 440 version; tag the release v1.2.3")


def macho_min_os(binary: Path) -> tuple[int, int]:
    """The minimum macOS version a darwin binary asks for, as (major, minor).

    macOS refuses to load a binary built for a newer system than it runs, and
    the linker writes that minimum into the Mach-O load commands. The wheel tag
    has to say at least that version, so read it from the binary instead of
    hardcoding a value that moves with the Go toolchain.
    """
    data = binary.read_bytes()
    magic = struct.unpack_from("<I", data, 0)[0]
    if magic in (MH_MAGIC, MH_MAGIC_64):
        # The load commands follow the header: 32 bytes for a 64-bit Mach-O
        # (the last field is reserved), 28 for a 32-bit one.
        offset = 32 if magic == MH_MAGIC_64 else 28
    else:
        raise BuildError(
            f"{binary} is not a thin Mach-O binary (magic {magic:#x}); "
            "cross-compile darwin with a single GOARCH"
        )

    ncmds = struct.unpack_from("<I", data, 16)[0]
    for _ in range(ncmds):
        cmd, size = struct.unpack_from("<II", data, offset)
        if cmd == LC_BUILD_VERSION:
            # cmd, cmdsize, platform, minos, sdk, ntools
            minos = struct.unpack_from("<I", data, offset + 12)[0]
        elif cmd == LC_VERSION_MIN_MACOSX:
            # cmd, cmdsize, version, sdk
            minos = struct.unpack_from("<I", data, offset + 8)[0]
        else:
            offset += size
            continue
        return (minos >> 16, (minos >> 8) & 0xFF)
    raise BuildError(f"{binary} carries no minimum macOS version")


def platform_tags(goos: str, goarch: str, binary: Path) -> tuple[str, ...]:
    """The wheel platform tags one binary can be published under.

    Linux binaries are built with CGO_ENABLED=0, which makes them static: the
    same file runs on glibc and musl systems, so it is published under both tag
    families. Without the musl tags, pip on Alpine would find no wheel at all.
    """
    if goos == "linux":
        arch = _arch(LINUX_ARCH, goarch, goos)
        return (f"manylinux_2_17_{arch}", f"musllinux_1_2_{arch}")
    if goos == "darwin":
        arch = _arch(DARWIN_ARCH, goarch, goos)
        major, minor = macho_min_os(binary)
        return (f"macosx_{major}_{minor}_{arch}",)
    if goos == "windows":
        return (f"win_{_arch(WINDOWS_ARCH, goarch, goos)}",)
    raise BuildError(f"no wheel platform tag for {goos}/{goarch}")


def _arch(known: dict[str, str], goarch: str, goos: str) -> str:
    try:
        return known[goarch]
    except KeyError:
        raise BuildError(f"no wheel platform tag for {goos}/{goarch}") from None


def targets_from_dist(dist: Path) -> dict[tuple[str, str], Path]:
    """The built binaries listed in the dist/artifacts.json GoReleaser writes."""
    manifest = dist / "artifacts.json"
    if not manifest.is_file():
        raise BuildError(
            f"{manifest} is missing; run goreleaser first, or pass --bin-dir"
        )

    targets: dict[tuple[str, str], Path] = {}
    for artifact in json.loads(manifest.read_text(encoding="utf-8")):
        if artifact.get("type") != "Binary":
            continue
        goos, goarch = artifact.get("goos"), artifact.get("goarch")
        if not goos or not goarch:
            continue
        # Anything but the released command is somebody else's artifact.
        if Path(artifact["path"]).stem != BINARY:
            continue
        # artifacts.json paths are relative to the dist folder; fall back to
        # the working directory so a manifest copied elsewhere still resolves.
        candidates = [dist / artifact["path"], Path(artifact["path"])]
        binary = next((path for path in candidates if path.is_file()), None)
        if binary is None:
            raise BuildError(f"{artifact['path']} from {manifest} is missing")
        targets[(goos, goarch)] = binary
    if not targets:
        raise BuildError(f"{manifest} lists no {BINARY} binaries")
    return targets


def targets_from_bin_dir(bin_dir: Path) -> dict[tuple[str, str], Path]:
    """The binaries `make build` writes, named <binary>-<goos>-<goarch>[.exe]."""
    targets: dict[tuple[str, str], Path] = {}
    for path in sorted(bin_dir.glob(f"{BINARY}-*")):
        if not path.is_file() or path.suffix.lower() not in ("", ".exe"):
            continue
        parts = path.stem.split("-")
        if len(parts) != 3 or parts[0] != BINARY:
            continue
        targets[(parts[1], parts[2])] = path
    if not targets:
        raise BuildError(
            f"{bin_dir} holds no {BINARY}-<goos>-<goarch> binaries; run `make cross-check`"
        )
    return targets


def metadata(version: str, readme: Path) -> str:
    """The METADATA file: the headers PyPI reads plus the README as the description."""
    headers = [
        "Metadata-Version: 2.4",
        f"Name: {DIST_NAME}",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        "License-Expression: Apache-2.0",
        "License-File: LICENSE",
        "Description-Content-Type: text/markdown",
        "Requires-Python: >=3.9",
        "Keywords: agents,ci,cli,git,review",
        *(f"Project-URL: {label}, {url}" for label, url in PROJECT_URLS),
        *(f"Classifier: {classifier}" for classifier in CLASSIFIERS),
    ]
    description = readme.read_text(encoding="utf-8").strip()
    return "\n".join(headers) + "\n\n" + description + "\n"


def wheel_metadata(tag: str) -> str:
    """The WHEEL file for a platform wheel."""
    return "\n".join(
        [
            "Wheel-Version: 1.0",
            "Generator: slopcop packaging/pypi/build_wheels.py",
            # The payload is a binary, so the wheel installs into platlib.
            "Root-Is-Purelib: false",
            f"Tag: py3-none-{tag}",
            "",
        ]
    )


def wheel_entries(
    version: str,
    tag: str,
    goos: str,
    binary: Path,
    shim: Path,
    readme: Path,
    license_file: Path,
) -> list[tuple[str, bytes, int]]:
    """Every member of one wheel, as (archive name, content, file mode)."""
    entries = [
        (f"{PACKAGE}/{path.name}", path.read_bytes(), 0o644)
        for path in sorted(shim.glob("*.py"))
    ]
    if not entries:
        raise BuildError(f"{shim} holds no launch module to package")
    # The binary keeps its executable mode: pip applies it on install, and
    # stat.S_IFREG is what makes pip read the mode at all.
    # The name is the launcher's, not the build's: `make build` names binaries
    # after their target, GoReleaser after the binary.
    packed = f"{BINARY}.exe" if goos == "windows" else BINARY
    entries.append((f"{PACKAGE}/_bin/{packed}", binary.read_bytes(), 0o755))

    info = f"{export_name(DIST_NAME)}-{version}.dist-info"
    entries.extend(
        [
            (f"{info}/METADATA", metadata(version, readme).encode(), 0o644),
            (f"{info}/WHEEL", wheel_metadata(tag).encode(), 0o644),
            (f"{info}/entry_points.txt", ENTRY_POINTS.encode(), 0o644),
            (f"{info}/licenses/{license_file.name}", license_file.read_bytes(), 0o644),
        ]
    )
    return entries


def write_wheel(path: Path, version: str, entries, epoch: int) -> None:
    """Write one wheel, RECORD last, so it can carry every other file's hash."""
    record = f"{export_name(DIST_NAME)}-{version}.dist-info/RECORD"
    lines = [
        f"{name},sha256={_digest(content)},{len(content)}"
        for name, content, _ in entries
    ]
    lines.append(f"{record},,")
    entries = [*entries, (record, ("\n".join(lines) + "\n").encode(), 0o644)]

    stamp = time.gmtime(epoch)[:6]
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as wheel:
        for name, content, mode in entries:
            member = zipfile.ZipInfo(name, date_time=stamp)
            # 3 is Unix, which is what carries the executable bit above.
            member.create_system = 3
            member.external_attr = (stat.S_IFREG | mode) << 16
            member.compress_type = zipfile.ZIP_DEFLATED
            wheel.writestr(member, content)


def _digest(content: bytes) -> str:
    digest = hashlib.sha256(content).digest()
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Build the PyPI wheels for one SlopCop release"
    )
    parser.add_argument(
        "--version",
        required=True,
        help="release tag (v1.2.3) or an already normalized PEP 440 version",
    )
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument(
        "--dist", type=Path, help="GoReleaser dist folder, read through artifacts.json"
    )
    source.add_argument(
        "--bin-dir", type=Path, help="folder of `make build` binaries"
    )
    parser.add_argument(
        "--out", type=Path, default=Path("pypi-dist"), help="where to write the wheels"
    )
    parser.add_argument(
        "--readme",
        type=Path,
        default=None,
        help="long description (default: the repository README.md)",
    )
    parser.add_argument(
        "--license",
        type=Path,
        default=None,
        help="license file to ship (default: the repository LICENSE)",
    )
    args = parser.parse_args(argv)

    repo = Path(__file__).resolve().parents[2]
    readme = args.readme or repo / "README.md"
    license_file = args.license or repo / "LICENSE"
    shim = Path(__file__).resolve().parent / PACKAGE

    version = pypi_version(args.version)
    targets = (
        targets_from_dist(args.dist)
        if args.dist
        else targets_from_bin_dir(args.bin_dir)
    )
    epoch = int(os.environ.get("SOURCE_DATE_EPOCH", ZIP_EPOCH))

    args.out.mkdir(parents=True, exist_ok=True)
    wheels: list[tuple[Path, str, str]] = []
    for (goos, goarch), binary in sorted(targets.items()):
        for tag in platform_tags(goos, goarch, binary):
            name = f"{export_name(DIST_NAME)}-{version}-py3-none-{tag}.whl"
            out = args.out / name
            write_wheel(
                out,
                version,
                wheel_entries(version, tag, goos, binary, shim, readme, license_file),
                epoch,
            )
            wheels.append((out, goos, goarch))

    for path, goos, goarch in wheels:
        size = path.stat().st_size / 1024 / 1024
        print(f"{path}\t{goos}/{goarch}\t{size:.1f} MB")
    print(f"{len(wheels)} wheels for {len(targets)} targets in {args.out}")
    return 0


def run() -> int:
    try:
        return main()
    except BuildError as error:
        print(f"build_wheels: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
