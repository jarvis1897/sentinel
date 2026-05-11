import grpc
import sys
import os
from dotenv import load_dotenv

load_dotenv()

CURRENT_DIR = os.path.dirname(os.path.abspath(__file__))
GEN_DIR = os.path.join(CURRENT_DIR, "gen")
sys.path.insert(0, GEN_DIR)
sys.path.insert(0, CURRENT_DIR)

from langchain_core.messages import HumanMessage, SystemMessage
from brain import get_app
import sentinel_pb2
import sentinel_pb2_grpc

SYSTEM_PROMPT = """You are the Sentinel autonomous infrastructure healing agent.
Your only tool is execute_system_fix, which runs a Docker command on the Go Muscle node.

Allowed commands (enforced by the Go policy engine):
  docker restart sentinel-nginx
  docker restart sentinel-redis
  docker start sentinel-nginx
  docker start sentinel-redis

Rules:
- Call execute_system_fix once with the appropriate restart command.
- If the tool returns SUCCESS, declare the incident resolved and stop.
- If the tool returns FAILURE or BLOCKED, try docker start instead, then stop.
- Never attempt more than 2 tool calls per incident.
- Do not explain what you are doing — just fix it."""


def handle_anomaly(app, anomaly_str: str):
    parts = anomaly_str.split("|", 2)
    if len(parts) != 3:
        print(f"[BRAIN] malformed anomaly message: {anomaly_str!r}")
        return

    container, error_type, log_snippet = parts
    print(f"\n{'='*60}")
    print(f"[BRAIN] INCIDENT: {error_type} on {container}")
    print(f"[BRAIN] Log: {log_snippet}")
    print(f"{'='*60}")

    inputs = {
        "messages": [
            SystemMessage(content=SYSTEM_PROMPT),
            HumanMessage(content=(
                f"ALERT: Container '{container}' is down.\n"
                f"Error type: {error_type}\n"
                f"Log: {log_snippet}\n"
                f"Fix it now."
            )),
        ]
    }

    for step in app.stream(inputs):
        node, state = next(iter(step.items()))
        last = state["messages"][-1]
        print(f"[{node.upper()}] {last.content or '(tool call)'}")


def run():
    print("[BRAIN] Starting Python Brain — waiting for anomalies from Go Muscle...")

    with grpc.insecure_channel("localhost:50051") as channel:
        stub = sentinel_pb2_grpc.SentinelServiceStub(channel)
        app = get_app(stub)

        log_request = sentinel_pb2.LogRequest(node_id="python-brain", line_count=0)

        try:
            for log_line in stub.StreamLogs(log_request):
                handle_anomaly(app, log_line.content)
        except grpc.RpcError as e:
            print(f"[BRAIN] Lost connection to Go Muscle: {e.details()}")


if __name__ == "__main__":
    run()
