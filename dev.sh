#!/bin/bash

echo "===== Container Viewer - Development Mode ====="

# Stop any existing containers
docker-compose -f docker-compose.dev.yml down --remove-orphans

# Start fresh container
echo "Starting development environment..."
docker-compose -f docker-compose.dev.yml up --build $@

# Clean up on exit
trap 'echo "Shutting down..."; docker-compose -f docker-compose.dev.yml down' INT TERM EXIT 