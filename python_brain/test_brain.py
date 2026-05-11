"""Unit tests for brain.py — tool creation and graph compilation."""
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "gen"))
sys.path.insert(0, os.path.dirname(__file__))

from unittest.mock import MagicMock, patch
from langchain_core.messages import AIMessage
import sentinel_pb2
from brain import create_tools, get_app


# --- execute_system_fix tool -------------------------------------------------

class _Stub:
    def __init__(self, response):
        self._resp = response

    def ExecuteAction(self, request):
        return self._resp


def test_tool_success():
    stub = _Stub(sentinel_pb2.ActionResponse(success=True, output="container restarted"))
    (execute,) = create_tools(stub)
    result = execute.invoke({"command": "docker restart sentinel-nginx"})
    assert "SUCCESS" in result
    assert "docker restart sentinel-nginx" in result
    assert "container restarted" in result


def test_tool_failure():
    stub = _Stub(sentinel_pb2.ActionResponse(success=False, output="no such container"))
    (execute,) = create_tools(stub)
    result = execute.invoke({"command": "docker restart sentinel-nginx"})
    assert "FAILURE" in result
    assert "no such container" in result


def test_tool_blocked_by_policy():
    stub = _Stub(sentinel_pb2.ActionResponse(
        success=False,
        output="blocked by policy engine: systemctl restart nginx",
        exit_code=-1,
    ))
    (execute,) = create_tools(stub)
    result = execute.invoke({"command": "systemctl restart nginx"})
    assert "FAILURE" in result
    assert "blocked by policy engine" in result


def test_tool_rpc_error():
    class _BrokenStub:
        def ExecuteAction(self, request):
            raise Exception("connection refused")

    (execute,) = create_tools(_BrokenStub())
    result = execute.invoke({"command": "docker restart sentinel-nginx"})
    assert "RPC ERROR" in result
    assert "connection refused" in result


# --- graph compilation -------------------------------------------------------

def test_get_app_compiles():
    stub = MagicMock()
    with patch("brain.ChatGoogleGenerativeAI") as MockLLM:
        mock_model = MagicMock()
        mock_model.bind_tools.return_value = mock_model
        MockLLM.return_value = mock_model

        app = get_app(stub)

    assert app is not None
    # Graph must expose the two nodes we rely on.
    assert "agent" in app.get_graph().nodes
    assert "tools" in app.get_graph().nodes
