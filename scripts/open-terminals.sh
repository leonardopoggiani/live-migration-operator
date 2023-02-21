#!/bin/bash

xfce4-terminal -T "ubuntu@172.16.3.227" -x bash -c "ssh -t ubuntu@172.16.3.227; bash" &
xfce4-terminal -T "ubuntu@172.16.6.52" -x bash -c "ssh -t ubuntu@172.16.6.52; bash" &
xfce4-terminal -T "ubuntu@172.16.3.79" -x bash -c "ssh -t ubuntu@172.16.3.79; bash" &
xfce4-terminal -T "ubuntu@172.16.6.63" -x bash -c "ssh -t ubuntu@172.16.6.63; bash" &

