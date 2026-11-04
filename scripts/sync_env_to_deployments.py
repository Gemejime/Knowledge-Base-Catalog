#!/usr/bin/env python3
"""
sync_env_to_deployments.py

Synchronizes and validates environment configuration between .env.prod (the single
source of truth for deployments) and Helm deployment values (deployments/helm/values.yaml).

Features:
- --sync: Updates values.yaml's env block from .env.prod.
- --check-only: Verifies zero drift between .env.prod and values.yaml (exits with 1 on drift, 0 if clean).
- Excludes sensitive keys (containing PASSWORD, SECRET, TOKEN, KEY, or credential URIs).
- Preserves 'secretName' in values.yaml and keeps all top-level Helm blocks and comments intact.
- Windows UTF-8 stdout formatting support.
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import sys
import yaml

# Enable UTF-8 console output for Windows compatibility
if hasattr(sys.stdout, "reconfigure"):
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
if hasattr(sys.stderr, "reconfigure"):
    try:
        sys.stderr.reconfigure(encoding="utf-8")
    except Exception:
        pass

SENSITIVE_PATTERNS = ["PASSWORD", "SECRET", "TOKEN", "KEY", "DATABASE_URL"]


def find_repo_root() -> Path:
    """Finds repository root by searching upward for go.mod or .git."""
    cur = Path(__file__).resolve().parent
    for p in [cur] + list(cur.parents):
        if (p / "go.mod").exists() or (p / ".git").exists():
            return p
    return cur.parent


def resolve_values_paths(repo_root: Path, explicit_path: Path | None = None) -> list[Path]:
    """Resolves target values.yaml paths in deployments/helm."""
    if explicit_path:
        return [explicit_path.resolve()]

    candidates = [
        repo_root / "deployments" / "helm" / "rag-kb-catalog-service" / "values.yaml",
        repo_root / "deployments" / "helm" / "values.yaml",
    ]
    existing = [p for p in candidates if p.exists()]
    if existing:
        return existing
    # Fallback to standard chart values path
    return [repo_root / "deployments" / "helm" / "rag-kb-catalog-service" / "values.yaml"]


def is_sensitive(key: str, value: str = "") -> bool:
    """Returns True if key or value represents sensitive credentials/tokens."""
    key_upper = key.upper()
    if any(pattern in key_upper for pattern in SENSITIVE_PATTERNS):
        return True
    if "://" in value and "@" in value and re.search(r"://[^:]+:[^@]+@", value):
        return True
    return False


def parse_env_file(env_path: Path) -> dict[str, str]:
    """Parses KEY=VALUE pairs from a dotenv file, handling quotes and comments."""
    if not env_path.exists():
        raise FileNotFoundError(f"Environment file not found: {env_path}")

    env_vars: dict[str, str] = {}
    content = env_path.read_text(encoding="utf-8")

    for line in content.splitlines():
        trimmed = line.strip()
        if not trimmed or trimmed.startswith("#"):
            continue

        if trimmed.startswith("export "):
            trimmed = trimmed[7:].strip()

        eq_idx = trimmed.find("=")
        if eq_idx == -1:
            continue

        key = trimmed[:eq_idx].strip()
        if not key:
            continue

        raw_val = trimmed[eq_idx + 1:].strip()

        # Handle quoted string values
        if len(raw_val) >= 2 and ((raw_val[0] == '"' and raw_val[-1] == '"') or (raw_val[0] == "'" and raw_val[-1] == "'")):
            quote_char = raw_val[0]
            val = raw_val[1:-1]
            if quote_char == '"':
                val = val.replace('\\"', '"').replace("\\n", "\n").replace("\\r", "\r").replace("\\t", "\t").replace("\\\\", "\\")
        else:
            # Inline comment truncation for unquoted values
            comment_idx = raw_val.find(" #")
            if comment_idx != -1:
                raw_val = raw_val[:comment_idx].strip()
            elif raw_val.startswith("#"):
                raw_val = ""
            val = raw_val

        env_vars[key] = val

    return env_vars


def update_values_file(values_path: Path, prod_env: dict[str, str]) -> None:
    """
    Updates the 'env:' section in values.yaml with non-sensitive keys from prod_env.
    Preserves 'secretName' in env: and keeps all other top-level Helm blocks and comments intact.
    """
    if not values_path.exists():
        raise FileNotFoundError(f"Helm values file not found: {values_path}")

    content = values_path.read_text(encoding="utf-8")
    parsed = yaml.safe_load(content)
    if not isinstance(parsed, dict):
        raise ValueError(f"Invalid YAML structure in {values_path}")

    existing_env = parsed.get("env") or {}
    preserved_secret_name = existing_env.get("secretName")

    # Filter out sensitive keys
    filtered_env: dict[str, str] = {}
    for k, v in prod_env.items():
        if is_sensitive(k, v):
            continue
        filtered_env[k] = v

    # Preserve secretName if present
    if preserved_secret_name is not None and "secretName" not in filtered_env:
        filtered_env["secretName"] = preserved_secret_name

    lines = content.splitlines(keepends=True)
    env_start_idx: int | None = None
    env_end_idx: int | None = None

    for idx, line in enumerate(lines):
        if re.match(r"^env:\s*(?:#.*)?$", line):
            env_start_idx = idx
            break

    new_env_lines = ["env:\n"]
    for k, v in filtered_env.items():
        escaped_v = str(v).replace("\\", "\\\\").replace('"', '\\"')
        new_env_lines.append(f'  {k}: "{escaped_v}"\n')

    if env_start_idx is None:
        updated_content = content + "\n" + "".join(new_env_lines)
    else:
        for idx in range(env_start_idx + 1, len(lines)):
            line = lines[idx]
            if re.match(r"^[a-zA-Z0-9_-]+:\s*", line):
                env_end_idx = idx
                break
        if env_end_idx is None:
            env_end_idx = len(lines)

        updated_lines = lines[:env_start_idx] + new_env_lines + lines[env_end_idx:]
        updated_content = "".join(updated_lines)

    # Validate output YAML integrity
    re_parsed = yaml.safe_load(updated_content)
    if not isinstance(re_parsed, dict) or "env" not in re_parsed:
        raise ValueError(f"Integrity check failed while rendering updated YAML for {values_path}")

    values_path.write_text(updated_content, encoding="utf-8")


def check_drift(env_path: Path, values_paths: list[Path]) -> bool:
    """
    Compares .env.prod against values.yaml.
    Returns True if drift detected (or security violation), False if zero drift.
    """
    prod_env = parse_env_file(env_path)
    expected_env: dict[str, str] = {}
    for k, v in prod_env.items():
        if is_sensitive(k, v):
            continue
        expected_env[k] = str(v)

    drift_detected = False

    for vp in values_paths:
        if not vp.exists():
            print(f"[ERROR] Target values file not found: {vp}", file=sys.stderr)
            drift_detected = True
            continue

        content = vp.read_text(encoding="utf-8")
        try:
            parsed = yaml.safe_load(content)
        except Exception as e:
            print(f"[ERROR] Failed to parse YAML in {vp}: {e}", file=sys.stderr)
            drift_detected = True
            continue

        if not isinstance(parsed, dict):
            print(f"[ERROR] Invalid YAML root structure in {vp}", file=sys.stderr)
            drift_detected = True
            continue

        helm_env = parsed.get("env") or {}
        if not isinstance(helm_env, dict):
            print(f"[ERROR] 'env' block in {vp} is not a dictionary", file=sys.stderr)
            drift_detected = True
            continue

        # Check for leaked secrets
        for k, v in helm_env.items():
            if is_sensitive(k, str(v)):
                print(f"[SECURITY VIOLATION] Sensitive credential '{k}' found in {vp}!", file=sys.stderr)
                drift_detected = True

        # Check for missing or mismatched keys from expected_env
        for k, expected_v in expected_env.items():
            if k not in helm_env:
                print(f"[DRIFT] {vp.name} missing key '{k}' (expected '{expected_v}')")
                drift_detected = True
            elif str(helm_env[k]) != expected_v:
                print(f"[DRIFT] {vp.name} mismatch for '{k}': expected '{expected_v}', got '{helm_env[k]}'")
                drift_detected = True

        # Check for unexpected extra keys (ignoring secretName)
        for k, val in helm_env.items():
            if k == "secretName":
                continue
            if k not in expected_env:
                print(f"[DRIFT] {vp.name} contains unexpected key '{k}'='{val}' not present in {env_path.name}")
                drift_detected = True

    return drift_detected


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Sync and validate environment variables between .env.prod and Helm deployments."
    )
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument(
        "--sync",
        action="store_true",
        help="Update deployments/helm/values.yaml from .env.prod.",
    )
    group.add_argument(
        "--check-only",
        action="store_true",
        help="Verify zero drift between .env.prod and deployments/helm/values.yaml.",
    )
    parser.add_argument(
        "--env-file",
        type=Path,
        default=None,
        help="Path to .env.prod (defaults to repo root .env.prod).",
    )
    parser.add_argument(
        "--values-file",
        type=Path,
        default=None,
        help="Path to Helm values.yaml (defaults to deployments/helm/.../values.yaml).",
    )

    args = parser.parse_args()
    repo_root = find_repo_root()

    env_path = args.env_file.resolve() if args.env_file else (repo_root / ".env.prod")
    values_paths = resolve_values_paths(repo_root, args.values_file)

    if not env_path.exists():
        print(f"[ERROR] Environment file not found: {env_path}", file=sys.stderr)
        return 1

    if args.sync:
        try:
            prod_env = parse_env_file(env_path)
            for vp in values_paths:
                update_values_file(vp, prod_env)
                print(f"[SYNC] Successfully updated {vp} from {env_path.name}")
            return 0
        except Exception as e:
            print(f"[ERROR] Synchronization failed: {e}", file=sys.stderr)
            return 1

    if args.check_only:
        drift = check_drift(env_path, values_paths)
        if drift:
            print("\n" + "=" * 80, file=sys.stderr)
            print(" [DRIFT DETECTED] Deployment configuration is out of sync with .env.prod!", file=sys.stderr)
            print(" Please synchronize Helm values by running:", file=sys.stderr)
            print("   python scripts/sync_env_to_deployments.py --sync", file=sys.stderr)
            print("=" * 80 + "\n", file=sys.stderr)
            return 1
        print(f"[OK] Zero drift detected between {env_path.name} and Helm deployment values.")
        return 0

    return 0


if __name__ == "__main__":
    sys.exit(main())

