#!/bin/bash

# Set the timeout period for the curl command
timeout_period=5

# Set the base URL for the service
base_url="http://"

# Verifies that the namespace name was provided as an argument
if [ $# -eq 0 ]; then
  echo "Usage: $0 <namespace>"
  exit 1
fi

# If the user enters "all", get a list of all namespaces
if [ "$1" == "all" ]; then
  namespaces=$(kubectl get namespaces -o jsonpath='{.items[*].metadata.name}')
else
  # Otherwise, use the specified namespace
  namespaces=$1
fi

# Loop through each namespace
for namespace in $namespaces; do
  echo "Namespace: $namespace"
  echo -e "Load Balancer\tPing\tCurl\tService"

  # Get a list of services in the specified namespace
  services=$(kubectl get services -n $namespace -o jsonpath='{.items[*].metadata.name}')

  # Loop through each service and get its load balancer IP (if it has one)
  for service in $services; do
    # Get the load balancer IP for the service (if it has one)
    load_balancer=$(kubectl get service $service -n $namespace -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

    if [ -z "$load_balancer" ]; then
      continue
    fi

    # Ping the load balancer to check for basic connectivity
    if ping -c 3 -W 1 "$load_balancer" > /dev/null 2>&1; then
      ping_status="\033[32m\u2714\033[0m"
    else
      ping_status="\033[31m\u2718\033[0m"
    fi

    # Try to make a curl request to the service within the timeout period
    if curl --silent --connect-timeout $timeout_period "${base_url}${load_balancer}" > /dev/null; then
      curl_status="\033[32m\u2714\033[0m"
      service_status="\033[32mup\033[0m"
    else
      curl_status="\033[31m\u2718\033[0m"
      service_status="\033[31mdown\033[0m"
    fi

    # Print the load balancer IP, ping status, curl status, and service status
    echo -e "$load_balancer\t$ping_status\t$curl_status\t$service_status\t$service"
  done
done
