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
        '"environment":"isolated"}'
        '] }',
        encoding="utf-8",
    )

    with pytest.raises(CatalogError, match=r"unclassified suites: suites/new\.json"):
        load_catalog(catalog_path=catalog_path, suites_dir=suites_dir)


def test_runnable_profile_excludes_hardware_and_all_keeps_every_case() -> None:
    catalog = load_catalog()

    runnable = select_cases(catalog, profile="runnable")
    hardware = select_cases(catalog, profile="hardware")
    all_cases = select_cases(catalog, profile="all")

    assert runnable
    assert hardware
    assert all_cases == catalog.cases
    assert len(runnable) == 14
    assert len(hardware) == 12
    assert len(all_cases) == 26
    assert not any(case.environment in {"adb", "external"} for case in runnable)
    assert {case.environment for case in runnable} <= {"isolated", "mobilegym"}
    assert {case.environment for case in hardware} <= {"adb", "external"}
    assert {case.environment for case in runnable}.isdisjoint(
        {case.environment for case in hardware}
    )
    assert {case.suite for case in runnable} | {case.suite for case in hardware} == {
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


def test_suite_selection_must_match_profile_unless_all() -> None:
    catalog = load_catalog()

    with pytest.raises(CatalogError, match="outside the runnable profile"):
        select_cases(
            catalog,
            profile="runnable",
            suite="suites/vphone_ios_basic.json",
        )


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


def test_workflow_schedules_all_runnable_cases_on_monday_wednesday_friday() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")

    assert "  push:\n" not in workflow
    assert "- cron: '17 18 * * 0,2,4'" in workflow
    assert "if: ${{ startsWith(github.ref, 'refs/heads/') }}" in workflow
    assert "github.ref == 'refs/heads/main'" not in workflow
    assert (
        'elif [[ "$EVENT_NAME" == "schedule" ]]; then\n            profile="runnable"'
        in workflow
    )


def test_workflow_maps_unprefixed_github_configuration_to_runner_environment() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")

    assert "${{ vars.AIDEN_" not in workflow
    assert "${{ secrets.AIDEN_" not in workflow
    expected_mappings = {
        "AIDEN_BENCHMARK_AGENT_PROVIDER": "vars.BENCHMARK_AGENT_PROVIDER",
        "AIDEN_BENCHMARK_AGENT_MODEL": "vars.BENCHMARK_AGENT_MODEL",
        "AIDEN_BENCHMARK_AGENT_BASE_URL": "vars.BENCHMARK_AGENT_BASE_URL",
        "AIDEN_BENCHMARK_AGENT_API_KEY": "secrets.BENCHMARK_AGENT_API_KEY",
        "AIDEN_BENCHMARK_JUDGE_MODEL": "vars.BENCHMARK_JUDGE_MODEL",
        "AIDEN_BENCHMARK_JUDGE_BASE_URL": "vars.BENCHMARK_JUDGE_BASE_URL",
        "AIDEN_BENCHMARK_JUDGE_API_KEY": "secrets.BENCHMARK_JUDGE_API_KEY",
        "AIDEN_DAEMON_IMAGE": "vars.DAEMON_IMAGE",
        "AIDEN_BENCHMARK_PHONE_ENVIRONMENT_URL": "vars.BENCHMARK_PHONE_ENVIRONMENT_URL",
        "AIDEN_BENCHMARK_IOS_ENVIRONMENT_URL": "vars.BENCHMARK_IOS_ENVIRONMENT_URL",
        "AIDEN_BENCHMARK_MAC_ENVIRONMENT_URL": "vars.BENCHMARK_MAC_ENVIRONMENT_URL",
        "AIDEN_BENCHMARK_VPHONE_ENVIRONMENT_URL": "vars.BENCHMARK_VPHONE_ENVIRONMENT_URL",
        "AIDEN_BENCHMARK_AIDEN_APP_IOS_ENVIRONMENT_URL": (
            "vars.BENCHMARK_AIDEN_APP_IOS_ENVIRONMENT_URL"
        ),
        "AIDEN_BENCHMARK_AIDEN_APP_ANDROID_ENVIRONMENT_URL": (
            "vars.BENCHMARK_AIDEN_APP_ANDROID_ENVIRONMENT_URL"
        ),
    }
    for environment_name, github_expression in expected_mappings.items():
        assert f"{environment_name}: ${{{{ {github_expression}" in workflow
