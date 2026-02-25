import grpc
import sys
import os

# adds the 'gen' folder to Python's path so it can find the code
GEN_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'gen')

sys.path.insert(0, GEN_DIR)

import sentinel_pb2
import sentinel_pb2_grpc

def run():
    print("Starting Python Brain...")
    
    with grpc.insecure_channel('localhost:50051') as channel:
        stub = sentinel_pb2_grpc.SentinelServiceStub(channel)

        # Example call to ReportAnomaly
        print("Reporting anomaly...")
        report = sentinel_pb2.AnomalyReport(
            source_node = "PythonBrain",
            error_type = "SIMULATED_CRASH",
            raw_log_snippet="Traceback: Memory Error at 0x004",
            timestamp = 123456789
        )

        response = stub.ReportAnomaly(report)
        print(f"Go muscle acknowledged: {response.acknowledgement_id}")

if __name__ == '__main__':
    run()