import pytest

from runner.environment_endpoint import EnvironmentEndpoint


def test_environment_endpoint_builds_all_urls_from_one_base_url():
    endpoint = EnvironmentEndpoint("http://127.0.0.1:19090/bridge/")

    assert endpoint.base == "http://127.0.0.1:19090/bridge"
    assert endpoint.health == "http://127.0.0.1:19090/bridge/health"
    assert endpoint.setup == "http://127.0.0.1:19090/bridge/api/setup"
    assert endpoint.release == "http://127.0.0.1:19090/bridge/api/release"
    assert endpoint.screen == "http://127.0.0.1:19090/bridge/api/screen"
    assert endpoint.concurrent == "http://127.0.0.1:19090/bridge/api/concurrent"


def test_environment_endpoint_does_not_guess_that_base_url_is_an_api_url():
    endpoint = EnvironmentEndpoint("http://127.0.0.1:19090/api/setup")

    assert endpoint.health == "http://127.0.0.1:19090/api/setup/health"
    assert endpoint.release == "http://127.0.0.1:19090/api/setup/api/release"


@pytest.mark.parametrize("url", ["", "127.0.0.1:19090", "ftp://127.0.0.1/bridge"])
def test_environment_endpoint_rejects_invalid_base_url(url):
    with pytest.raises(ValueError, match="invalid environment base URL"):
        EnvironmentEndpoint(url)
