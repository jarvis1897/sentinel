import grpc
import sys
import os
from dotenv import load_dotenv

load_dotenv()

CURRENT_DIR = os.path.dirname(os.path.abspath(__file__))
GEN_DIR = os.path.join(CURRENT_DIR, "gen")
sys.path.insert(0, GEN_DIR)

from langchain_core.messages import HumanMessage
from python_brain.brain import get_app
import sentinel_pb2
import sentinel_pb2_grpc

def run():
    print("Starting Python Brain...")
    
    with grpc.insecure_channel('localhost:50051') as channel:
        stub = sentinel_pb2_grpc.SentinelServiceStub(channel)

        app = get_app(stub)

        report = sentinel_pb2.AnomalyReport(
            source_node="PythonBrain",
            error_type="NGINX_DOWN",
            raw_log_snippet="connect() failed (111: Connection refused) while connecting to upstream",
            timestamp=123456789
        )

        response = stub.ReportAnomaly(report)
        print(f"Go muscle acknowledged: {response.acknowledgement_id}")

        if response.is_being_processed:
            print("--- Brain is taking control ---")
            inputs = {"messages": [HumanMessage(content=f"Fix this: {report.raw_log_snippet}")]}
            
            for output in app.stream(inputs):
                # This will now actually trigger the gRPC call back to Go!
                print(output)

if __name__ == '__main__':
    run()