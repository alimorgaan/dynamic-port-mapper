#!/bin/bash
set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Building and starting dynamic-port-mapper in production mode...${NC}"

# Stop and remove existing container if it exists
echo -e "${GREEN}Cleaning up any existing containers...${NC}"
docker-compose down 2>/dev/null || true

# Build the Docker image
echo -e "${GREEN}Building Docker image...${NC}"
docker-compose build

# Start the container
echo -e "${GREEN}Starting container...${NC}"
docker-compose up -d

# Show container status
echo -e "${GREEN}Container status:${NC}"
docker-compose ps

# Wait a moment for the container to start
sleep 2

# Show container logs but don't follow (exit after showing logs)
echo -e "${GREEN}Container logs:${NC}"
docker-compose logs --tail=20

echo -e "${YELLOW}Dynamic Port Mapper is now running at http://localhost:5000${NC}"
echo -e "${YELLOW}To stop the container, run: docker-compose down${NC}" 