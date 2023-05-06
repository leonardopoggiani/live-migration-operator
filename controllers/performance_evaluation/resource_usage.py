import psutil
import subprocess
import time

# Define the command to run the Go program
cmd = ["./my_go_program"]

# Start the Go program as a subprocess
process = subprocess.Popen(cmd)

# Wait for the process to start up and stabilize
time.sleep(5)

# Monitor the resource usage of the process for 60 seconds
for i in range(60):
    # Get the process object for the Go program
    p = psutil.Process(process.pid)

    # Get the CPU usage as a percentage
    cpu_percent = p.cpu_percent()

    # Get the memory usage in bytes
    memory_usage = p.memory_info().rss

    # Get the disk I/O usage in bytes
    disk_io = p.io_counters().read_bytes + p.io_counters().write_bytes

    # Print the resource usage metrics
    print(f"Time: {i}, CPU: {cpu_percent:.2f}%, Memory: {memory_usage/1024/1024:.2f}MB, Disk I/O: {disk_io/1024/1024:.2f}MB")

    # Wait for one second before checking again
    time.sleep(1)

# Terminate the subprocess
process.terminate()
