"""Unit tests for main.py — anomaly parsing and handle_anomaly logic."""
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "gen"))
sys.path.insert(0, os.path.dirname(__file__))

from unittest.mock import MagicMock, Mock
from langchain_core.messages import SystemMessage, HumanMessage
from main import handle_anomaly, SYSTEM_PROMPT


def _mock_app(*contents):
    """Build an app mock whose stream yields one step per content string."""
    steps = []
    for content in contents:
        msg = Mock()
        msg.content = content
        steps.append({"agent": {"messages": [msg]}})

    app = MagicMock()
    app.stream.return_value = iter(steps)
    return app


def test_valid_anomaly_calls_stream():
    app = _mock_app("Incident resolved.")
    handle_anomaly(app, "sentinel-nginx|NGINX_DOWN|nginx is down")
    app.stream.assert_called_once()


def test_valid_anomaly_passes_container_in_human_message():
    app = _mock_app("done")
    handle_anomaly(app, "sentinel-redis|REDIS_DOWN|redis unreachable")

    inputs = app.stream.call_args[0][0]
    human = inputs["messages"][1]
    assert isinstance(human, HumanMessage)
    assert "sentinel-redis" in human.content
    assert "REDIS_DOWN" in human.content


def test_system_prompt_is_first_message():
    app = _mock_app("done")
    handle_anomaly(app, "sentinel-nginx|NGINX_DOWN|nginx down")

    inputs = app.stream.call_args[0][0]
    system = inputs["messages"][0]
    assert isinstance(system, SystemMessage)
    assert system.content == SYSTEM_PROMPT


def test_system_prompt_contains_allowed_commands():
    assert "docker restart sentinel-nginx" in SYSTEM_PROMPT
    assert "docker restart sentinel-redis" in SYSTEM_PROMPT
    assert "docker start sentinel-nginx" in SYSTEM_PROMPT
    assert "docker start sentinel-redis" in SYSTEM_PROMPT


def test_malformed_no_pipes_skips_stream():
    app = MagicMock()
    handle_anomaly(app, "this-has-no-pipes")
    app.stream.assert_not_called()


def test_malformed_one_pipe_skips_stream():
    app = MagicMock()
    handle_anomaly(app, "sentinel-nginx|NGINX_DOWN")  # missing log snippet
    app.stream.assert_not_called()


def test_log_snippet_is_included_in_human_message():
    app = _mock_app("done")
    handle_anomaly(app, "sentinel-nginx|NGINX_DOWN|connection refused on port 80")

    inputs = app.stream.call_args[0][0]
    human = inputs["messages"][1]
    assert "connection refused on port 80" in human.content
