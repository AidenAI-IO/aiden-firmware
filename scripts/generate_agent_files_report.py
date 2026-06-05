#!/usr/bin/env python3
"""
Generate HTML visualization for agent memory and skills on Luckfox device.
Scans /userdata/agent/memory/ and /userdata/agent/skills/ directories.
"""
import json
import os
from pathlib import Path
from datetime import datetime
import html
import sys


def escape(s: str) -> str:
    """HTML escape a string."""
    return html.escape(str(s)) if s else ""


def scan_directory(base_path: Path, show_hidden: bool = True) -> dict:
    """Recursively scan a directory and return file structure."""
    result = {
        'path': str(base_path),
        'exists': base_path.exists(),
        'files': [],
        'total_size': 0,
    }

    if not base_path.exists():
        return result

    try:
        for item in sorted(base_path.rglob('*')):
            if item.is_file():
                # Skip hidden files unless requested
                if not show_hidden and item.name.startswith('.'):
                    continue

                relative_path = item.relative_to(base_path)
                file_info = {
                    'name': item.name,
                    'path': str(item),
                    'relative_path': str(relative_path),
                    'size': item.stat().st_size,
                    'modified': datetime.fromtimestamp(item.stat().st_mtime).isoformat(),
                    'is_hidden': item.name.startswith('.'),
                }

                # Try to read text content
                try:
                    if item.suffix in ['.md', '.txt', '.json', '.jsonl', '.toml', '.yaml', '.yml', '']:
                        content = item.read_text('utf-8')
                        file_info['content'] = content
                        file_info['type'] = 'text'

                        # Parse JSON
                        if item.suffix == '.json':
                            try:
                                file_info['json'] = json.loads(content)
                            except (json.JSONDecodeError, ValueError):
                                # Invalid JSON, skip parsing
                                pass

                        # Parse JSONL (JSON Lines)
                        if item.suffix == '.jsonl':
                            try:
                                lines = []
                                for line in content.strip().split('\n'):
                                    if line.strip():
                                        lines.append(json.loads(line))
                                file_info['jsonl'] = lines
                                file_info['jsonl_count'] = len(lines)
                            except (json.JSONDecodeError, ValueError):
                                # Invalid JSONL, skip parsing
                                pass

                        # Extract references from YAML-like content
                        file_info['references'] = extract_references(content, str(relative_path))
                except (UnicodeDecodeError, IOError):
                    # Binary file or read error, mark as binary
                    file_info['type'] = 'binary'

                result['files'].append(file_info)
                result['total_size'] += file_info['size']
    except Exception as e:
        result['error'] = str(e)

    return result


def extract_references(content: str, current_file: str) -> list:
    """Extract file references from content, excluding self-references."""
    refs = []

    # Get current file's ID (e.g., "proc_832cbf216c9e" from "device/procedures/proc_832cbf216c9e.yaml")
    import re
    current_id = None
    id_match = re.search(r'(ep_\d+_[a-f0-9]+|proc_[a-f0-9]+|app_[a-f0-9]+|fail_[a-f0-9]+)', current_file)
    if id_match:
        current_id = id_match.group(1)

    # Extract episode references: ep_XXXXX
    episode_pattern = r'ep_\d+_[a-f0-9]+'
    for match in re.finditer(episode_pattern, content):
        ep_id = match.group(0)
        # Skip self-references
        if ep_id == current_id:
            continue
        if ep_id not in [r['id'] for r in refs]:
            refs.append({
                'type': 'episode',
                'id': ep_id,
                'path': f'episodes/*/{ep_id}/*'
            })

    # Extract procedure references: proc_XXXXX
    proc_pattern = r'proc_[a-f0-9]+'
    for match in re.finditer(proc_pattern, content):
        proc_id = match.group(0)
        if proc_id == current_id:
            continue
        if proc_id not in [r['id'] for r in refs]:
            refs.append({
                'type': 'procedure',
                'id': proc_id,
                'path': f'device/procedures/{proc_id}.yaml'
            })

    # Extract app references: app_XXXXX
    app_pattern = r'app_[a-f0-9]+'
    for match in re.finditer(app_pattern, content):
        app_id = match.group(0)
        if app_id == current_id:
            continue
        if app_id not in [r['id'] for r in refs]:
            refs.append({
                'type': 'app',
                'id': app_id,
                'path': f'device/apps/{app_id}.yaml'
            })

    # Extract failure references: fail_XXXXX
    fail_pattern = r'fail_[a-f0-9]+'
    for match in re.finditer(fail_pattern, content):
        fail_id = match.group(0)
        if fail_id == current_id:
            continue
        if fail_id not in [r['id'] for r in refs]:
            refs.append({
                'type': 'failure',
                'id': fail_id,
                'path': f'device/failures/{fail_id}.yaml'
            })

    return refs


def format_size(size_bytes: int) -> str:
    """Format file size in human readable format."""
    for unit in ['B', 'KB', 'MB', 'GB']:
        if size_bytes < 1024.0:
            return f"{size_bytes:.1f} {unit}"
        size_bytes /= 1024.0
    return f"{size_bytes:.1f} TB"


def generate_html_report(memory_data: dict, skills_data: dict) -> str:
    """Generate self-contained HTML report."""
    # Don't use .format() since template contains CSS with braces
    # Instead, we'll do simple string replacement in main()
    return HTML_TEMPLATE


def main():
    """Main entry point."""
    import argparse

    parser = argparse.ArgumentParser(
        description='Generate HTML visualization for agent memory and skills'
    )
    parser.add_argument(
        '--memory-dir',
        default='/userdata/agent/memory',
        help='Memory directory path (default: /userdata/agent/memory)'
    )
    parser.add_argument(
        '--skills-dir',
        default='/userdata/agent/skills',
        help='Skills directory path (default: /userdata/agent/skills)'
    )
    parser.add_argument(
        '--skill-state-dir',
        default='/userdata/agent/skill-state',
        help='Skill-state directory path (default: /userdata/agent/skill-state)'
    )
    parser.add_argument(
        '--output',
        '-o',
        default='/userdata/agent/files_report.html',
        help='Output HTML file path (default: /userdata/agent/files_report.html)'
    )
    parser.add_argument(
        '--show-hidden',
        action='store_true',
        default=True,
        help='Show hidden files (default: true)'
    )
    args = parser.parse_args()

    # Load HTML template
    script_dir = Path(__file__).parent
    template_path = script_dir / 'agent_files_template.html'

    if not template_path.exists():
        print(f"Error: Template not found at {template_path}", file=sys.stderr)
        sys.exit(1)

    global HTML_TEMPLATE
    HTML_TEMPLATE = template_path.read_text('utf-8')

    # Scan directories
    print(f"Scanning memory directory: {args.memory_dir}")
    memory_data = scan_directory(Path(args.memory_dir), args.show_hidden)

    print(f"Scanning skills directory: {args.skills_dir}")
    skills_data = scan_directory(Path(args.skills_dir), args.show_hidden)

    print(f"Scanning skill-state directory: {args.skill_state_dir}")
    skill_state_data = scan_directory(Path(args.skill_state_dir), args.show_hidden)

    # Generate HTML
    html = generate_html_report(memory_data, skills_data)

    # Replace placeholders
    html = html.replace('{{TIMESTAMP}}', datetime.now().strftime('%Y-%m-%d %H:%M:%S'))
    html = html.replace('{{MEMORY_COUNT}}', str(len(memory_data.get('files', []))))
    html = html.replace('{{SKILLS_COUNT}}', str(len(skills_data.get('files', []))))
    html = html.replace('{{MEMORY_SIZE}}', format_size(memory_data.get('total_size', 0)))
    html = html.replace('{{SKILLS_SIZE}}', format_size(skills_data.get('total_size', 0)))

    memory_json = json.dumps(memory_data, ensure_ascii=False, indent=2).replace('</', r'<\/')
    skills_json = json.dumps(skills_data, ensure_ascii=False, indent=2).replace('</', r'<\/')
    skill_state_json = json.dumps(skill_state_data, ensure_ascii=False, indent=2).replace('</', r'<\/')
    html = html.replace('{{MEMORY_JSON}}', memory_json)
    html = html.replace('{{SKILLS_JSON}}', skills_json)
    html = html.replace('{{SKILL_STATE_JSON}}', skill_state_json)

    # Write output
    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(html, encoding='utf-8')

    print(f"\n✓ Report generated: {output_path.absolute()}")
    print(f"  Memory files: {len(memory_data.get('files', []))} ({format_size(memory_data.get('total_size', 0))})")
    print(f"  Skills files: {len(skills_data.get('files', []))} ({format_size(skills_data.get('total_size', 0))})")
    print(f"  Skill-state files: {len(skill_state_data.get('files', []))} ({format_size(skill_state_data.get('total_size', 0))})")

    if not memory_data.get('exists'):
        print(f"  ⚠ Memory directory not found: {args.memory_dir}")
    if not skills_data.get('exists'):
        print(f"  ⚠ Skills directory not found: {args.skills_dir}")


HTML_TEMPLATE = None  # Will be loaded from file


if __name__ == '__main__':
    main()

