import os
from typing import Annotated, TypedDict, List
from langchain_google_genai import ChatGoogleGenerativeAI
from langchain_core.messages import BaseMessage, HumanMessage, SystemMessage
from langgraph.graph import StateGraph, END
from langgraph.prebuilt import ToolNode
from langchain_core.tools import tool
import sentinel_pb2
import sentinel_pb2_grpc

def create_tools(grpc_stub):
    """
    Factory function to create tools that have access to the gRPC connection.
    """
    # --- 1. Define the Tool (The Bridge back to Go) ---
    # Wrap the grpc call inside a @tool so the LLM can use it
    @tool
    def execute_system_fix(command: str):
        """
        Executes a shell command on the remote Go Sentinel node to fix a system error.
        Example commands: 'systemctl restart nginx', 'rm -rf /tmp/cache/*'
        """
        # --- The actual gRPC call ---
        request = sentinel_pb2.ActionRequest(
            action_id="ai-fix-" + os.urandom(4).hex(),
            command = command
        )

        try:
            # Calling the Go service
            response = grpc_stub.ExecuteAction(request)
            if response.success:
                return f"SUCCESS: Go Sentinel executed '{command}'. Output: {response.output}"
            else:
                return f"FAILURE: Go Sentinel failed to run '{command}'. Error: {response.output}"
        except Exception as e:
            return f"RPC ERROR: Could not connect to Go Muscle: {str(e)}"
        
    return [execute_system_fix]

# We will compile the graph inside a function now so we can inject the stub
def get_app(grpc_stub):
    tools = create_tools(grpc_stub)
    model = ChatGoogleGenerativeAI(
        model="gemini-2.5-flash",
        temperature=0,
        google_api_key=os.getenv("GOOGLE_API_KEY")
    ).bind_tools(tools)

    # --- 2. Define the Graph State ---
    class AgentState(TypedDict):
        messages: Annotated[List[BaseMessage], lambda x, y: x + y]

    # --- 3. Define the Graph Nodes ---
    def call_model(state: AgentState):
        response = model.invoke(state["messages"])
        return {"messages": [response]}

    # --- 4. Build the State Graph ---
    workflow = StateGraph(AgentState)
    
    # Add our reasoning node
    workflow.add_node("agent", call_model)
    # Add our action node (the tool)
    workflow.add_node("tools", ToolNode(tools))
    
    # Set the flow: Start -> Agent -> (if tool call) -> Tools -> Agent -> (if finished) -> End
    workflow.set_entry_point("agent")
    
    def should_continue(state: AgentState):
        last_message = state["messages"][-1]
        if last_message.tool_calls:
            return "tools"
        return END
    
    workflow.add_conditional_edges("agent", should_continue)
    workflow.add_edge("tools", "agent")
    
    return workflow.compile()