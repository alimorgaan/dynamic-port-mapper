.PHONY: build run run-port compose docker-build docker-run dev dev-stop test-setup test-cleanup clean help

# Variables
APP_NAME=dynamic-port-mapper
PROD_CONTAINER_NAME=container-viewer
DEV_CONTAINER_NAME=container-viewer-dev
BINARY_NAME=dynamic-port-mapper
DOCKER_IMAGE=alimorgaan/dynamic-port-mapper
VERSION=1.0.0

# Build the application locally
build:
	go build -o $(APP_NAME) .

# Run the application locally
run: build
	./$(APP_NAME)

# Run the application with specific port
run-port: build
	@echo "Running $(BINARY_NAME) on port 8080..."
	@./$(BINARY_NAME) -port 8080

# Run a Docker Compose project with automatic port mapping
compose: build
	@echo "Running Docker Compose with automatic port mapping..."
	@./$(BINARY_NAME) compose test-projects/app1/docker-compose.yml up -d

# Build the Docker image
docker-build:
	@echo "Building Docker image $(DOCKER_IMAGE):$(VERSION)..."
	@docker build -t $(DOCKER_IMAGE):$(VERSION) .
	@docker tag $(DOCKER_IMAGE):$(VERSION) $(DOCKER_IMAGE):latest

# Run the application in Docker
docker-run: docker-build
	@echo "Running Docker container from $(DOCKER_IMAGE):$(VERSION)..."
	@docker run -d --name $(BINARY_NAME) \
		-p 5000:5000 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_IMAGE):$(VERSION)

# Start development mode with hot reloading
dev:
	@echo "Starting development environment..."
	docker-compose -f docker-compose.dev.yml up --build

# Run development mode in the background
dev-detach:
	@echo "Starting development environment in the background..."
	docker-compose -f docker-compose.dev.yml up --build -d

# Start production mode
prod:
	@echo "Starting production environment..."
	docker-compose up --build

# Run production mode in the background
prod-detach:
	@echo "Starting production environment in the background..."
	docker-compose up --build -d

# Stop all containers
stop:
	@echo "Stopping all containers..."
	-docker-compose -f docker-compose.dev.yml down --remove-orphans
	-docker-compose down --remove-orphans

# Show status of containers
status:
	@echo "Docker Containers:"
	@docker ps -a | grep -E "$(PROD_CONTAINER_NAME)|$(DEV_CONTAINER_NAME)" || echo "No containers found"

# Show logs
logs-dev:
	docker-compose -f docker-compose.dev.yml logs -f

logs-prod:
	docker-compose logs -f

# Stop the development environment
dev-stop:
	@echo "Stopping development environment..."
	@docker-compose -f docker-compose.dev.yml down

# Start test Docker Compose projects to demonstrate port conflicts
test-setup:
	@echo "Starting test Docker Compose projects..."
	@cd test-projects/app1 && docker-compose up -d
	@cd test-projects/app2 && docker-compose up -d

# Clean up test Docker Compose projects
test-cleanup:
	@echo "Cleaning up test Docker Compose projects..."
	@cd test-projects/app1 && docker-compose down
	@cd test-projects/app2 && docker-compose down

# Clean build artifacts
clean:
	rm -f $(APP_NAME)
	rm -rf tmp/
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@docker rm -f $(BINARY_NAME) 2>/dev/null || true

# Help
help:
	@echo "Dynamic Port Mapper Makefile Commands:"
	@echo "  make build      - Build the application locally"
	@echo "  make run        - Run the application locally"
	@echo "  make run-port   - Run the application on port 8080"
	@echo "  make compose    - Run Docker Compose with automatic port mapping"
	@echo "  make dev        - Start development environment with hot reloading"
	@echo "  make dev-detach - Start development environment in background"
	@echo "  make prod       - Start production environment"
	@echo "  make prod-detach - Start production environment in background"
	@echo "  make stop       - Stop all containers"
	@echo "  make status     - Show status of containers"
	@echo "  make logs-dev   - Show development logs"
	@echo "  make logs-prod  - Show production logs"
	@echo "  make dev-stop   - Stop the development environment"
	@echo "  make test-setup - Start test Docker Compose projects"
	@echo "  make test-cleanup - Clean up test Docker Compose projects"
	@echo "  make clean      - Clean up"
	@echo "  make help       - Show this help" 