import os

def break_system():
    # We create a 'failure' flag that our Go node will 'detect'
    with open("system_status.txt", "w") as f:
        f.write("ERROR: NGINX_SERVICE_STOPPED")
    print("!!! System broken: system_status.txt created !!!")

if __name__ == "__main__":
    break_system()