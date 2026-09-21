#!/bin/sh
# Fails if the running Swarm executes a different inode than /Applications/Swarm.app has on disk.
pid=$(pgrep -x Swarm) || { echo "Swarm not running"; exit 0; }
run=$(lsof -p "$pid" 2>/dev/null | awk '$4=="txt" {print $8; exit}')
disk=$(ls -i /Applications/Swarm.app/Contents/MacOS/Swarm | awk '{print $1}')
[ "$run" = "$disk" ] && echo "fresh ($run)" || { echo "STALE: running $run, on disk $disk"; exit 1; }
