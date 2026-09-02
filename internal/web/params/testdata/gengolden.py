#!/usr/bin/env python3
"""Regenerate internal/web/params/testdata/golden.json from the Python originals.

Run from the notebooklm-py/ repository root:

    uv run python -m notebooklm-py/notebooklm-go/internal/web/params/testdata/gengolden \\
        > internal/web/params/testdata/golden.json

Or directly from this file's directory:

    python3 /path/to/gengolden.py > /path/to/golden.json

This script is the regeneration seam for the golden-bytes table; the
test driver (internal/web/params/notebooks_test.go::TestBuilders_GoldenBytes)
reads golden.json and asserts every byte matches what the Go builder
produces through wire.Marshal. The committed golden.json is the
contract; this script is the way the contract is built.

Per AGENTS.md rule 1, the Python builders are normative — every value
in this table is generated from the actual builder function in
notebooklm-py/src/notebooklm/_web/params/notebooks.py or the inline
literal in _web/notebooks.py / _web/sharing.py.
"""

from __future__ import annotations

import json
import os
import sys
from typing import Any


def _build_list() -> list[Any]:
    """Port of _web/notebooks.py::WebNotebooksAPI.list line 499."""
    return [None, 1, None, [2]]


def _build_delete(notebook_id: str) -> list[Any]:
    """Port of _web/notebooks.py::WebNotebooksAPI.delete line 756."""
    return [[notebook_id], [2]]


def _build_summary(notebook_id: str) -> list[Any]:
    """Port of _web/notebooks.py::WebNotebooksAPI.get_summary line 806."""
    return [notebook_id, [2]]


def _build_share(notebook_id: str, public: bool) -> list[Any]:
    """Port of _web/sharing.py::WebSharingAPI.set_public line 88."""
    access = 1 if public else 0
    return [
        [[notebook_id, None, [access], [access, ""]]],
        1,
        None,
        [2],
    ]


def _build_remove_collaborator(notebook_id: str, email: str) -> list[Any]:
    """Port of _web/sharing.py::_share_params line 168 with a single _REMOVE entry."""
    return [
        [[notebook_id, [[email, None, 4]], None, [0, ""]]],
        0,  # notify=false
        None,
        [2],
    ]


def _build_set_share_access(notebook_id: str, level: int) -> list[Any]:
    """Port of _web/sharing.py::WebSharingAPI.set_view_level line 122."""
    return [
        notebook_id,
        [[None, None, None, None, None, None, None, None, [[level]]]],
    ]


def _build_remove_recent(notebook_id: str) -> list[Any]:
    """Port of _web/notebooks.py::WebNotebooksAPI.remove_from_recent line 886."""
    return [notebook_id]


def _make_entry(name: str, method: str, rpc_id: str, params: list[Any]) -> dict:
    # ensure_ascii=False mirrors the Go wire.Marshal default
    # (SetEscapeHTML(false)) — the Python original's json.dumps encodes
    # non-ASCII codepoints as \uXXXX by default, but the Go encoder keeps
    # them as raw UTF-8. The golden table must match what Go emits, not
    # what Python's json module emits by default.
    body = json.dumps([[method, params, rpc_id, None, None]], separators=(",", ":"), ensure_ascii=False)
    return {
        "name": name,
        "method": method,
        "rpc_id": rpc_id,
        "params": params,
        "expected": body,
    }


def _main() -> int:
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "..", "notebooklm-py", "src"))
    from notebooklm._web.params.notebooks import (
        build_create_notebook_params,
        build_get_notebook_params,
        build_update_notebook_params,
    )

    cases = [
        _make_entry("BuildList", "rpc", "wXbhsf", _build_list()),
        _make_entry("BuildCreate", "rpc", "CCqFvf", build_create_notebook_params("My Notebook")),
        _make_entry("BuildDelete", "rpc", "WWINqb", _build_delete("nb-abc-123")),
        _make_entry(
            "BuildRename_title_only",
            "rpc",
            "s0tc2d",
            build_update_notebook_params("nb-abc-123", title="New Title"),
        ),
        _make_entry(
            "BuildRename_title_emoji",
            "rpc",
            "s0tc2d",
            build_update_notebook_params("nb-abc-123", title="New Title", emoji="📓"),
        ),
        _make_entry(
            "BuildRename_emoji_only",
            "rpc",
            "s0tc2d",
            build_update_notebook_params("nb-abc-123", title=None, emoji="📓"),
        ),
        _make_entry("BuildGet", "rpc", "rLM1Ne", build_get_notebook_params("nb-abc-123")),
        _make_entry("BuildSummary", "rpc", "VfAZjd", _build_summary("nb-abc-123")),
        _make_entry("BuildShare_public", "rpc", "QDyure", _build_share("nb-abc-123", True)),
        _make_entry("BuildShare_restricted", "rpc", "QDyure", _build_share("nb-abc-123", False)),
        _make_entry("BuildUnshare", "rpc", "QDyure", _build_share("nb-abc-123", False)),
        _make_entry("BuildGetShareStatus", "rpc", "JFMDGd", _build_summary("nb-abc-123")),
        _make_entry(
            "BuildRemoveCollaborator",
            "rpc",
            "QDyure",
            _build_remove_collaborator("nb-abc-123", "alice@example.com"),
        ),
        _make_entry(
            "BuildSetShareAccess_full",
            "rpc",
            "s0tc2d",
            _build_set_share_access("nb-abc-123", 0),
        ),
        _make_entry(
            "BuildSetShareAccess_chat",
            "rpc",
            "s0tc2d",
            _build_set_share_access("nb-abc-123", 1),
        ),
        _make_entry("BuildRemoveRecentlyViewed", "rpc", "fejl7e", _build_remove_recent("nb-abc-123")),
    ]

    table = {
        "_comment": (
            "Byte-exact expected EncodeRequest output for every notebook builder "
            "in internal/web/params/notebooks.go. Generated by running the Python "
            "originals through json.dumps(separators=(',', ':')). Regenerate via "
            "internal/web/params/testdata/gengolden.py."
        ),
        "builders": cases,
    }
    json.dump(table, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(_main())
