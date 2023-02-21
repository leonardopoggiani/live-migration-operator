#!/bin/bash

# Set the timeout period for the curl command
timeout_period=5

# Set the base URL for the service
base_url="http://"

echo "All cluster IP addresses:"
echo -e "Load Balancer\tPing\tCurl\tService"

# Get a list of all cluster IP addresses
cluster_ips=$(kubectl get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}')

# Loop through each cluster IP and ping and curl it
for cluster_ip in $cluster_ips; do
  # Ping the cluster IP to check for basic connectivity
  if ping -c 3 -W 1 "$cluster_ip" > /dev/null 2>&1; then
    ping_status="\033[32m\u2714\033[0m"
  else
    ping_status="\033[31m\u2718\033[0m"
  fi

  # Try to make a curl request to the service within the timeout period
  if curl --silent --connect-timeout $timeout_period "${base_url}${cluster_ip}" > /dev/null; then
    curl_status="\033[32m\u2714\033[0m"
    service_status="\033[32mup\033[0m"
  else
    curl_status="\033[31m\u2718\033[0m"
    service_status="\033[31mdown\033[0m"
  fi

  # Print the cluster IP address, ping status, curl status, and service status
  echo -e "$cluster_ip\t$ping_status\t$curl_status\t$service_status"
done
