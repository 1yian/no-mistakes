#!/usr/bin/env python3
import ast
import os
import subprocess
import sys

SCOPES = (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)


def python_files() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.py"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout
    return [name for name in out.split("\0") if name and os.path.exists(name)]


def docstrings(path: str):
    with open(path, encoding="utf-8") as handle:
        tree = ast.parse(handle.read(), path)
    for node in ast.walk(tree):
        if not isinstance(node, SCOPES) or not node.body:
            continue
        first = node.body[0]
        if (
            isinstance(first, ast.Expr)
            and isinstance(first.value, ast.Constant)
            and isinstance(first.value.value, str)
        ):
            yield first.lineno


def main() -> int:
    found = 0
    for path in python_files():
        for line in docstrings(path):
            print(f"{path}:{line}: docstring; docstrings are not allowed")
            found += 1
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
