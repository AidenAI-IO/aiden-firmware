import re
from pathlib import Path

import pytest

from ci import run_case as run_case_module
from ci.plan import CatalogError, load_catalog, select_cases
from ci.run_case import (
    _effective_exit_code,
    _environment_url,
    _images_prepared,
    _runtime_environment,
)


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


def test_ci_maps_unprefixed_action_variables_to_benchmark_runtime() -> None:
    runtime = _runtime_environment(
        {
            "BENCHMARK_AGENT_MODEL": "agent-model",
            "BENCHMARK_JUDGE_API_KEY": "judge-key",
            "DAEMON_IMAGE": "agent-daemon:test",
            "LANGFUSE_PUBLIC_KEY": "langfuse-key",
        }
    )

    assert runtime["AIDEN_BENCHMARK_AGENT_MODEL"] == "agent-model"
    assert runtime["AIDEN_BENCHMARK_JUDGE_API_KEY"] == "judge-key"
    assert runtime["AIDEN_DAEMON_IMAGE"] == "agent-daemon:test"
    assert runtime["LANGFUSE_PUBLIC_KEY"] == "langfuse-key"


def test_ci_uses_prepared_images_only_when_explicitly_enabled() -> None:
    assert _images_prepared({"BENCHMARK_CI_IMAGES_PREPARED": "1"}) is True
    assert _images_prepared({}) is False


def test_mobilegym_case_reuses_prepared_image(monkeypatch) -> None:
    case = next(
        item for item in load_catalog().cases if item.environment == "mobilegym"
    )
    captured = {}

    def fake_run_json(command, *, environment):
        captured["command"] = command
        captured["environment"] = environment
        return {"environment_url": "http://127.0.0.1:19090"}

    monkeypatch.setattr(run_case_module, "_run_json", fake_run_json)

    _environment_url(
        case,
        run_id="ci-test",
        environment={"BENCHMARK_CI_IMAGES_PREPARED": "1"},
    )

    assert "--no-build-mobilegym-image" in captured["command"]


def test_benchmark_case_reuses_prepared_daemon_image(monkeypatch, tmp_path: Path) -> None:
    case = next(item for item in load_catalog().cases if item.environment == "isolated")
    captured = {}

    def fake_run(command, **kwargs):
        captured["command"] = command
        return run_case_module.subprocess.CompletedProcess(command, 0)

    monkeypatch.setattr(run_case_module.subprocess, "run", fake_run)
    monkeypatch.setattr(run_case_module, "BENCHMARK_ROOT", tmp_path)

    run_case_module.run_case(
        case,
        run_id="ci-test",
        environment={"BENCHMARK_CI_IMAGES_PREPARED": "1"},
    )

    assert "--no-build-daemon-image" in captured["command"]


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
    artifact_paths = "\n".join(
        section.split("if-no-files-found:", 1)[0]
        for section in workflow.split("path:")[1:]
    )

    assert "cli-services" not in artifact_paths
    assert "workers" not in artifact_paths
    assert "auto-agent-setup.log" in artifact_paths


def test_workflow_checks_docker_and_surfaces_setup_failures() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")

    assert "docker version" in workflow
    assert "docker compose version" in workflow
    assert "docker info" in workflow
    assert "tail -n 400 \"$log\"" in workflow


def test_workflow_schedules_all_runnable_cases_on_monday_wednesday_friday() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")

    assert "  push:\n    branches:\n      - feat/benchmark-ci\n" in workflow
    assert "- cron: '17 18 * * 0,2,4'" in workflow
    self_hosted_guard = (
        "if: ${{ startsWith(github.ref, 'refs/heads/') "
        "&& github.event_name != 'pull_request' "
        "&& github.event_name != 'pull_request_target' }}"
    )
    assert workflow.count(self_hosted_guard) == 2
    assert "github.ref == 'refs/heads/main'" not in workflow
    assert (
        'elif [[ "$EVENT_NAME" == "schedule" || "$EVENT_NAME" == "push" ]]; then\n'
        '            profile="runnable"'
        in workflow
    )


def test_workflow_maps_unprefixed_github_configuration_to_runner_environment() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")

    assert re.search(r"(?m)^\s+AIDEN_[A-Z0-9_]+:", workflow) is None
    expected_mappings = {
        "BENCHMARK_AGENT_PROVIDER": "vars.BENCHMARK_AGENT_PROVIDER",
        "BENCHMARK_AGENT_MODEL": "vars.BENCHMARK_AGENT_MODEL",
        "BENCHMARK_AGENT_BASE_URL": "vars.BENCHMARK_AGENT_BASE_URL",
        "BENCHMARK_AGENT_API_KEY": "secrets.BENCHMARK_AGENT_API_KEY",
        "BENCHMARK_JUDGE_MODEL": "vars.BENCHMARK_JUDGE_MODEL",
        "BENCHMARK_JUDGE_BASE_URL": "vars.BENCHMARK_JUDGE_BASE_URL",
        "BENCHMARK_JUDGE_API_KEY": "secrets.BENCHMARK_JUDGE_API_KEY",
        "DAEMON_IMAGE": "vars.DAEMON_IMAGE",
        "BENCHMARK_PHONE_ENVIRONMENT_URL": "vars.BENCHMARK_PHONE_ENVIRONMENT_URL",
        "BENCHMARK_IOS_ENVIRONMENT_URL": "vars.BENCHMARK_IOS_ENVIRONMENT_URL",
        "BENCHMARK_MAC_ENVIRONMENT_URL": "vars.BENCHMARK_MAC_ENVIRONMENT_URL",
        "BENCHMARK_VPHONE_ENVIRONMENT_URL": "vars.BENCHMARK_VPHONE_ENVIRONMENT_URL",
        "BENCHMARK_AIDEN_APP_IOS_ENVIRONMENT_URL": (
            "vars.BENCHMARK_AIDEN_APP_IOS_ENVIRONMENT_URL"
        ),
        "BENCHMARK_AIDEN_APP_ANDROID_ENVIRONMENT_URL": (
            "vars.BENCHMARK_AIDEN_APP_ANDROID_ENVIRONMENT_URL"
        ),
    }
    for environment_name, github_expression in expected_mappings.items():
        assert f"{environment_name}: ${{{{ {github_expression}" in workflow


def test_workflow_uses_mirrored_container_sources() -> None:
    workflow = (
        Path(__file__).resolve().parents[2] / ".github" / "workflows" / "benchmark.yml"
    ).read_text(encoding="utf-8")
    compose = (
        Path(__file__).resolve().parents[1]
        / "docker"
        / "docker-compose.agent-daemon.yml"
    ).read_text(encoding="utf-8")

    assert (
        "DAEMON_DOCKERFILE: benchmark/docker/Dockerfile.agent-daemon.cn" in workflow
    )
    assert "MOBILEGYM_NODE_IMAGE: swr.cn-north-4.myhuaweicloud.com/" in workflow
    assert (
        "dockerfile: ${DAEMON_DOCKERFILE:-benchmark/docker/Dockerfile.agent-daemon}"
        in compose
    )
    assert "prepare-images:" in workflow
    assert "- prepare-images" in workflow
    assert "BENCHMARK_CI_IMAGES_PREPARED: '1'" in workflow
