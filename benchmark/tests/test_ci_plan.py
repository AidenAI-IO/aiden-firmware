from pathlib import Path

import pytest

from ci.plan import CatalogError, load_catalog, select_cases
from ci.run_case import _effective_exit_code, _environment_url


def test_catalog_rejects_an_unclassified_suite(tmp_path: Path) -> None:
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    (suites_dir / "known.json").write_text('{"name":"known","tasks":[]}', encoding="utf-8")
    (suites_dir / "new.json").write_text('{"name":"new","tasks":[]}', encoding="utf-8")
    catalog_path = tmp_path / "suites.json"
    catalog_path.write_text(
        '{"version":1,"cases":['
        '{"id":"known","suite":"suites/known.json",'
        '"environment":"isolated","cadence":"smoke"}'
        '] }',
        encoding="utf-8",
    )

    with pytest.raises(CatalogError, match=r"unclassified suites: suites/new\.json"):
        load_catalog(catalog_path=catalog_path, suites_dir=suites_dir)


def test_weekly_profile_excludes_hardware_and_all_keeps_every_case() -> None:
    catalog = load_catalog()

    weekly = select_cases(catalog, profile="weekly")
    hardware = select_cases(catalog, profile="hardware")
    all_cases = select_cases(catalog, profile="all")

    assert weekly
    assert hardware
    assert all_cases == catalog.cases
    assert not any(case.cadence == "hardware" for case in weekly)
    assert {case.suite for case in weekly} | {case.suite for case in hardware} == {
        case.suite for case in catalog.cases
    }


def test_selecting_a_suite_preserves_platform_specific_cases() -> None:
    catalog = load_catalog()

    selected = select_cases(
        catalog,
        profile="all",
        suite="suites/aiden_app/connection_capabilities_v1.json",
    )

    assert {case.target_platform for case in selected} == {"ios", "android"}


def test_external_case_reads_its_named_environment_variable() -> None:
    case = next(
        item
        for item in load_catalog().cases
        if item.id == "aiden-app-ios"
    )

    url, service = _environment_url(
        case,
        run_id="test-run",
        environment={case.environment_variable: "http://bridge.test:9000"},
    )

    assert url == "http://bridge.test:9000"
    assert service is None


def test_ci_rejects_a_run_where_every_task_was_skipped(tmp_path: Path) -> None:
    manifest = tmp_path / "manifest.json"
    manifest.write_text(
        '{"totals":{"tasks":3,"passed":0,"failed":0,"skipped":3,'
        '"judge_error":0,"timeout":0}}',
        encoding="utf-8",
    )

    assert _effective_exit_code(0, manifest) == 1


def test_ci_rejects_a_partial_run_with_an_infrastructure_skip(tmp_path: Path) -> None:
    manifest = tmp_path / "manifest.json"
    manifest.write_text(
        '{"totals":{"tasks":2,"passed":1,"failed":0,"skipped":1,'
        '"judge_error":0,"timeout":0}}',
        encoding="utf-8",
    )
    (tmp_path / "results.jsonl").write_text(
        '{"status":"passed","metrics":{}}\n'
        '{"status":"skipped","metrics":{"error":"agent not ready"}}\n',
        encoding="utf-8",
    )

    assert _effective_exit_code(0, manifest) == 1


def test_ci_allows_platform_ineligible_skips(tmp_path: Path) -> None:
    manifest = tmp_path / "manifest.json"
    manifest.write_text(
        '{"totals":{"tasks":2,"passed":1,"failed":0,"skipped":1,'
        '"judge_error":0,"timeout":0}}',
        encoding="utf-8",
    )
    (tmp_path / "results.jsonl").write_text(
        '{"status":"passed","metrics":{}}\n'
        '{"status":"skipped","metrics":{"error":"task platforms ios do not include target platform android"}}\n',
        encoding="utf-8",
    )

    assert _effective_exit_code(0, manifest) == 0


def test_workflow_artifacts_do_not_include_materialized_worker_configs() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")
    artifact_paths = workflow.split("path:", 1)[1].split("if-no-files-found:", 1)[0]

    assert "cli-services" not in artifact_paths
    assert "workers" not in artifact_paths
