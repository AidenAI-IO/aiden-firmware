import pytest

from ci.prepare_images import _matrix_environments


def test_matrix_environments_finds_selected_environment_types() -> None:
    assert _matrix_environments(
        '{"include":['
        '{"id":"memory","environment":"isolated"},'
        '{"id":"mobilegym","environment":"mobilegym"}'
        "]}"
    ) == {"isolated", "mobilegym"}


@pytest.mark.parametrize("matrix_json", ["{}", '{"include":{}}', "not-json"])
def test_matrix_environments_rejects_invalid_matrix(matrix_json: str) -> None:
    with pytest.raises(ValueError, match="matrix"):
        _matrix_environments(matrix_json)
