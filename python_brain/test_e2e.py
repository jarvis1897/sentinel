"""
End-to-end simulation: real gRPC server (mock Go Muscle) + mock LLM.

Tests the complete healing loop without needing real Docker or a Gemini key:
  anomaly string → handle_anomaly → LangGraph → tool call → gRPC → mock Muscle
  → SUCCESS response → LangGraph → final answer.
"""
import re
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "gen"))
sys.path.insert(0, os.path.dirname(__file__))

import grpc
import pytest
from concurrent import futures
from unittest.mock import patch

from langchain_core.messages import AIMessage
import sentinel_pb2
import sentinel_pb2_grpc
from brain import get_app
from main import handle_anomaly

# ---- mock Go Muscle gRPC server --------------------------------------------

_POLICY = re.compile(r"^docker (restart|start) sentinel-[a-z0-9-]+$")


class _MockMuscle(sentinel_pb2_grpc.SentinelServiceServicer):
    """Simulates the Go Muscle: enforces the policy and records calls."""

    def __init__(self):
        self.action_calls: list[str] = []

    def ExecuteAction(self, request, context):
        cmd = request.command.strip()
        self.action_calls.append(cmd)
        if _POLICY.match(cmd):
            container = cmd.split()[-1]
            return sentinel_pb2.ActionResponse(
                success=True, output=container, exit_code=0
            )
        return sentinel_pb2.ActionResponse(
            success=False,
            output=f"blocked by policy engine: {cmd}",
            exit_code=-1,
        )

    def ReportAnomaly(self, request, context):
        return sentinel_pb2.AnomalyResponse(
            acknowledgement_id="test-ack", is_being_processed=True
        )

    def StreamLogs(self, request, context):
        return iter([])


@pytest.fixture
def muscle():
    """Starts a real in-process gRPC server and yields (stub, servicer)."""
    servicer = _MockMuscle()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    sentinel_pb2_grpc.add_SentinelServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("localhost:0")
    server.start()

    channel = grpc.insecure_channel(f"localhost:{port}")
    stub = sentinel_pb2_grpc.SentinelServiceStub(channel)

    yield stub, servicer

    channel.close()
    server.stop(grace=0)


# ---- mock LLMs --------------------------------------------------------------

class _MockLLM:
    """call 1 → tool call, call 2 → final answer."""
    def __init__(self, command: str):
        self._command = command
        self._call = 0

    def bind_tools(self, tools):
        return self

    def invoke(self, messages):
        self._call += 1
        if self._call == 1:
            return AIMessage(
                content="",
                tool_calls=[{
                    "id": "call_001",
                    "name": "execute_system_fix",
                    "args": {"command": self._command},
                    "type": "tool_call",
                }],
            )
        return AIMessage(content="Incident resolved.")


class _MockLLMWithRetry:
    """call 1 → wrong command (blocked), call 2 → correct command, call 3 → done."""
    def __init__(self):
        self._call = 0

    def bind_tools(self, tools):
        return self

    def invoke(self, messages):
        self._call += 1
        if self._call == 1:
            return AIMessage(
                content="",
                tool_calls=[{
                    "id": "call_001",
                    "name": "execute_system_fix",
                    "args": {"command": "systemctl restart nginx"},
                    "type": "tool_call",
                }],
            )
        if self._call == 2:
            return AIMessage(
                content="",
                tool_calls=[{
                    "id": "call_002",
                    "name": "execute_system_fix",
                    "args": {"command": "docker restart sentinel-nginx"},
                    "type": "tool_call",
                }],
            )
        return AIMessage(content="Incident resolved after retry.")


# ---- tests ------------------------------------------------------------------

def test_e2e_nginx_healing(muscle):
    stub, servicer = muscle
    with patch("brain.ChatGoogleGenerativeAI", return_value=_MockLLM("docker restart sentinel-nginx")):
        app = get_app(stub)
        handle_anomaly(app, "sentinel-nginx|NGINX_DOWN|nginx health check failed")

    assert len(servicer.action_calls) == 1
    assert servicer.action_calls[0] == "docker restart sentinel-nginx"


def test_e2e_redis_healing(muscle):
    stub, servicer = muscle
    with patch("brain.ChatGoogleGenerativeAI", return_value=_MockLLM("docker restart sentinel-redis")):
        app = get_app(stub)
        handle_anomaly(app, "sentinel-redis|REDIS_DOWN|redis TCP check failed")

    assert len(servicer.action_calls) == 1
    assert servicer.action_calls[0] == "docker restart sentinel-redis"


def test_e2e_self_correction(muscle):
    """LLM tries a blocked command, sees FAILURE, corrects to an allowed one."""
    stub, servicer = muscle
    with patch("brain.ChatGoogleGenerativeAI", return_value=_MockLLMWithRetry()):
        app = get_app(stub)
        handle_anomaly(app, "sentinel-nginx|NGINX_DOWN|nginx is down")

    assert len(servicer.action_calls) == 2
    assert servicer.action_calls[0] == "systemctl restart nginx"    # blocked
    assert servicer.action_calls[1] == "docker restart sentinel-nginx"  # corrected


def test_e2e_policy_blocks_injection(muscle):
    stub, servicer = muscle
    with patch("brain.ChatGoogleGenerativeAI",
               return_value=_MockLLM("docker restart sentinel-nginx; rm -rf /")):
        app = get_app(stub)
        handle_anomaly(app, "sentinel-nginx|NGINX_DOWN|nginx is down")

    assert len(servicer.action_calls) == 1
    # Verify the mock Muscle returned blocked (policy simulation works)
    assert not _POLICY.match(servicer.action_calls[0])
