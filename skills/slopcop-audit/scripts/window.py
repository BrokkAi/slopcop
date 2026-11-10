#!/usr/bin/env python3
"""Split a commit range into per-module audit windows for `slopcop check`.

Subcommands:
  plan     <repo> <base> <head> [--max-lines N] [--max-windows N]
           Print JSON windows: {name, paths, files, lines}. Groups changed
           files by directory, splitting any group over --max-lines one level
           deeper, and merging the smallest groups when over --max-windows.
  prepare  <repo> <base> <head> <workdir> <name> <path>...
           Make a detached worktree at <base> under <workdir>/<name> and apply
           only <path>'s part of base..head to it. Prints the worktree path.
  cleanup  <repo> <workdir>
           Remove every worktree under <workdir>.

Never runs code from the audited repository; it only calls git.
"""
import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys


def git(repo, *args, data=None, check=True):
    r = subprocess.run(["git", "-C", repo, *args], input=data,
                       capture_output=True, check=False)
    if check and r.returncode != 0:
        sys.exit(f"git {' '.join(args)}: {r.stderr.decode(errors='replace').strip()}")
    return r.stdout


def changed(repo, base, head):
    """[(path, lines)] for base..head; renames count under the new path."""
    out = git(repo, "diff", "--numstat", "-z", "--find-renames", base, head)
    parts = out.decode(errors="replace").split("\0")
    files, i = [], 0
    while i < len(parts) and parts[i]:
        added, deleted, path = parts[i].split("\t", 2)
        if path == "":  # rename: next two fields are old and new
            path = parts[i + 2]
            i += 3
        else:
            i += 1
        lines = 0 if added == "-" else int(added) + int(deleted)
        files.append((path, max(lines, 1)))
    return files


def matches(path, globs):
    """A glob (`*` crosses `/`) or a plain directory prefix."""
    return any(fnmatch.fnmatch(path, g) or path.startswith(g.rstrip("/") + "/")
               for g in globs)


def load_modules(path):
    with open(path) as f:
        data = json.load(f)
    mods = data["modules"] if isinstance(data, dict) else data
    for m in mods:
        if not m.get("name") or not m.get("paths"):
            sys.exit(f"{path}: every module needs a name and paths")
    return mods


def by_directory(files, max_lines):
    pending = [(1, k, v) for k, v in group(files, 1).items()]
    out = []
    while pending:
        depth, key, members = pending.pop()
        total = sum(l for _, l in members)
        sub = group(members, depth + 1)
        if total > max_lines and len(sub) > 1:
            pending.extend((depth + 1, k, v) for k, v in sub.items())
            continue
        out.extend(chunk(key, members, max_lines) if total > max_lines
                   else [(key, members)])
    return out


def excluded(repo, head, files, patterns):
    """Drop files matching --exclude globs or marked linguist-generated/vendored."""
    paths = [p for p, _ in files]
    attrs = {}
    if paths:
        out = git(repo, "check-attr", "-z", "--source", head,
                  "linguist-generated", "linguist-vendored", "--stdin",
                  data="\0".join(paths).encode() + b"\0",
                  check=False).decode(errors="replace").split("\0")
        for i in range(0, len(out) - 2, 3):
            if out[i + 2] in ("set", "true"):
                attrs[out[i]] = out[i + 1]
    keep, dropped = [], []
    for path, lines in files:
        if path in attrs or matches(path, patterns):
            dropped.append((path, lines))
        else:
            keep.append((path, lines))
    return keep, dropped


def chunk(key, members, limit):
    """Split one directory's files into runs of at most `limit` lines."""
    out, cur, total = [], [], 0
    for path, lines in sorted(members):
        if cur and total + lines > limit:
            out.append((f"{key}#{len(out) + 1}", cur))
            cur, total = [], 0
        cur.append((path, lines))
        total += lines
    if cur:
        out.append((f"{key}#{len(out) + 1}" if out else key, cur))
    return out


def group(files, depth):
    groups = {}
    for path, lines in files:
        segs = path.split("/")
        key = "/".join(segs[:min(depth, len(segs) - 1)]) or "(root)"
        groups.setdefault(key, []).append((path, lines))
    return groups


def plan(args):
    files, dropped = excluded(args.repo, args.head,
                              changed(args.repo, args.base, args.head), args.exclude)
    windows, unmapped = [], files
    if args.modules:
        # Each file belongs to the first module whose paths match it.
        unmapped, assigned = [], {}
        mods = load_modules(args.modules)
        for path, lines in files:
            mod = next((m for m in mods if matches(path, m["paths"])), None)
            if mod is None:
                unmapped.append((path, lines))
            else:
                assigned.setdefault(mod["name"], []).append((path, lines))
        for mod in mods:
            members = assigned.get(mod["name"])
            if not members:
                continue
            for name, part in chunk(mod["name"], members, args.max_lines):
                windows.append({"name": name, "module": mod["name"],
                                "description": mod.get("description", ""),
                                "files": [p for p, _ in part],
                                "lines": sum(l for _, l in part)})
    for name, part in by_directory(unmapped, args.max_lines):
        windows.append({"name": name, "module": None, "files": [p for p, _ in part],
                        "lines": sum(l for _, l in part)})
    # Merge the smallest windows until under the cap, unmapped ones first so
    # mapped modules stay whole.
    # A merge never makes a slice over --max-lines; when no pair fits, the
    # plan goes over --max-windows and says so.
    merged = 0
    while len(windows) > args.max_windows:
        pairs = sorted(
            ((i, j) for i in range(len(windows)) for j in range(i + 1, len(windows))
             if windows[i]["lines"] + windows[j]["lines"] <= args.max_lines),
            key=lambda ij: (windows[ij[0]]["module"] != windows[ij[1]]["module"],
                            windows[ij[0]]["module"] is not None
                            or windows[ij[1]]["module"] is not None,
                            windows[ij[0]]["lines"] + windows[ij[1]]["lines"]))
        if not pairs:
            break
        i, j = pairs[0]
        b, a = windows.pop(j), windows.pop(i)
        merged += 1
        same = a["module"] if a["module"] == b["module"] else None
        windows.append({"name": f"{a['name']}+{b['name']}", "module": same,
                        "description": a.get("description", "") if same else "",
                        "files": a["files"] + b["files"],
                        "lines": a["lines"] + b["lines"]})
    windows.sort(key=lambda w: w["name"])
    for i, w in enumerate(windows, 1):
        w["slug"] = f"{i:02d}-" + re.sub(r"[^A-Za-z0-9._-]+", "_", w["name"])[:60]
        w["paths"] = w.pop("files")
    json.dump({"base": git(args.repo, "rev-parse", args.base).decode().strip(),
               "head": git(args.repo, "rev-parse", args.head).decode().strip(),
               "files": len(files), "lines": sum(l for _, l in files),
               "excluded": {"files": len(dropped), "lines": sum(l for _, l in dropped),
                            "paths": sorted({p.rsplit("/", 1)[0] if "/" in p else p
                                             for p, _ in dropped})[:50]},
               "merges": merged,
               "overCap": len(windows) > args.max_windows,
               "unmappedFiles": len(unmapped) if args.modules else None,
               "oversized": [w["slug"] for w in windows if w["lines"] > args.max_lines],
               "windows": windows}, sys.stdout, indent=2)
    print()


def prepare(args):
    wt = os.path.abspath(os.path.join(args.workdir, args.name))
    if os.path.exists(wt):
        sys.exit(f"{wt} already exists; run cleanup first")
    os.makedirs(args.workdir, exist_ok=True)
    git(args.repo, "worktree", "add", "--detach", "--quiet", wt, args.base)
    patch = git(args.repo, "diff", "--binary", "--find-renames",
                args.base, args.head, "--", *args.paths)
    if not patch:
        sys.exit("no changes for these paths in the range")
    git(wt, "apply", "--whitespace=nowarn", "-", data=patch)
    print(wt)


def cleanup(args):
    root = os.path.abspath(args.workdir)
    out = git(args.repo, "worktree", "list", "--porcelain").decode()
    for line in out.splitlines():
        if line.startswith("worktree ") and line[9:].startswith(root + os.sep):
            git(args.repo, "worktree", "remove", "--force", line[9:], check=False)
            print(f"removed {line[9:]}")
    git(args.repo, "worktree", "prune")


def main():
    p = argparse.ArgumentParser()
    sp = p.add_subparsers(dest="cmd", required=True)
    a = sp.add_parser("plan")
    a.add_argument("repo"); a.add_argument("base"); a.add_argument("head")
    a.add_argument("--max-lines", type=int, default=3000)
    a.add_argument("--max-windows", type=int, default=8)
    a.add_argument("--modules", help="JSON module map: {modules: [{name, paths, description}]}")
    a.add_argument("--exclude", action="append", default=[],
                   help="glob or directory prefix to leave out (repeatable)")
    a.set_defaults(fn=plan)
    b = sp.add_parser("prepare")
    b.add_argument("repo"); b.add_argument("base"); b.add_argument("head")
    b.add_argument("workdir"); b.add_argument("name"); b.add_argument("paths", nargs="+")
    b.set_defaults(fn=prepare)
    c = sp.add_parser("cleanup")
    c.add_argument("repo"); c.add_argument("workdir")
    c.set_defaults(fn=cleanup)
    args = p.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
