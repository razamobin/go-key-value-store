#!/bin/bash
echo "Sending shutdown signal to server..."
nc -z localhost 8081 || telnet localhost 8081
echo "Shutdown signal sent. Server should be stopping."