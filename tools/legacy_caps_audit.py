#!/usr/bin/env python3
"""Verify that files mentioning legacy also include LEGACY in explanatory text."""

from __future__ import annotations

import argparse
import os
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
LEGACY_RE = re.compile(r"legacy", re.IGNORECASE)
TEXT_EXTENSIONS = {
    ".c",
    ".cpp",
    ".css",
    ".go",
    ".h",
    ".hpp",
    ".hs",
    ".html",
    ".java",
    ".js",
    ".json",
    ".lua",
    ".md",
    ".py",
    ".rb",
    ".rs",
    ".sh",
    ".sql",
    ".tf",
    ".toml",
    ".ts",
    ".tsx",
    ".yaml",
    ".yml",
}
SKIP_DIRS = {
    ".git",
    "node_modules",
    "target",
    "dist",
    "build",
    "__pycache__",
}
SKIP_FILES = {
    "frontend/tsconfig.tsbuildinfo",
}


def is_probably_text(path: Path) -> bool:
    if path.suffix in TEXT_EXTENSIONS:
        return True
    return path.name in {"Makefile", "Dockerfile", ".gitignore"}


def iter_files(root: Path) -> list[Path]:
    files: list[Path] = []
    for current_root, dirnames, filenames in os.walk(root):
        dirnames[:] = [name for name in dirnames if name not in SKIP_DIRS]
        for filename in filenames:
            path = Path(current_root) / filename
            rel = path.relative_to(root).as_posix()
            if rel in SKIP_FILES or not is_probably_text(path):
                continue
            files.append(path)
    return files


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return path.read_text(encoding="utf-8", errors="replace")


def has_legacy_comment(path: Path, text: str) -> bool:
    if "LEGACY" not in text:
        return False

    if path.suffix in {".md", ".yaml", ".yml", ".json", ".toml", ".sql"}:
        return True

    in_block_comment = False
    for line in text.splitlines():
        stripped = line.strip()
        if "/*" in stripped:
            in_block_comment = True
        if "LEGACY" in stripped and (
            in_block_comment
            or stripped.startswith(("#", "//", "--", "*", "/*", "*/"))
            or "<!--" in stripped
        ):
            return True
        if "*/" in stripped:
            in_block_comment = False
    return False


def audit(root: Path) -> list[Path]:
    violations: list[Path] = []
    for path in iter_files(root):
        text = read_text(path)
        if LEGACY_RE.search(text) and not has_legacy_comment(path, text):
            violations.append(path.relative_to(root))
    return violations


def main() -> int:
    parser = argparse.ArgumentParser(description="Audit LEGACY comment coverage.")
    parser.add_argument("--root", default=str(ROOT), help="repository root to scan")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    violations = audit(root)
    if violations:
        print("Files mention legacy but do not contain LEGACY in explanatory text:")
        for path in violations:
            print(f"- {path}")
        return 1

    print("LEGACY comment audit passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
