#!/bin/bash
go run main.go &
echo $! > ./pid.txt
echo "Server started. PID saved in pid.txt"