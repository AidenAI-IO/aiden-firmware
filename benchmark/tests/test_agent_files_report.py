import importlib.util
import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT_PATH = REPO_ROOT / "scripts" / "generate_agent_files_report.py"


def load_report_module():
    spec = importlib.util.spec_from_file_location("generate_agent_files_report", SCRIPT_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_report_warns_when_skill_state_directory_missing(monkeypatch, tmp_path: Path, capsys):
    memory_dir = tmp_path / "memory"
    skills_dir = tmp_path / "skills"
    skill_state_dir = tmp_path / "skill-state"
    output = tmp_path / "files_report.html"
    for path in (memory_dir, skills_dir):
        path.mkdir(parents=True)

    module = load_report_module()
    monkeypatch.setattr(sys, "argv", [
        "generate_agent_files_report.py",
        "--memory-dir", str(memory_dir),
        "--skills-dir", str(skills_dir),
        "--skill-state-dir", str(skill_state_dir),
        "--output", str(output),
    ])

    module.main()

    out = capsys.readouterr().out
    assert "Skill-state directory not found" in out
