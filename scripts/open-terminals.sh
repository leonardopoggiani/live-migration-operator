#!/bin/bash

# Verifies that the file name was provided as an argument
if [ $# -eq 0 ]; then
  echo "Usage: $0 <file_name>"
  exit 1
fi

# Reads the file line by line and opens an SSH connection to each in a different terminal window
count=1
while read address; do
  xfce4-terminal -- /bin/bash -c "ssh -tt -o 'StrictHostKeyChecking=no' ubuntu@$address 'echo ubuntu | sudo -S su; bash'" &
  count=$((count+1))
done < $1
