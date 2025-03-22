# Dynamic Port Mapper for Docker

A tool for monitoring and managing Docker container port mappings, with intelligent handling of port conflicts for Docker Compose projects.

## Features

- **Real-time Container Monitoring**: Track all Docker containers running on your system, including their port mappings
- **Smart Port Conflict Detection**: Automatically detects port conflicts between Docker containers
- **Dynamic Port Remapping**: Intelligently remaps ports to avoid conflicts between Docker Compose projects
- **Web Interface**: Clean, responsive web UI to view all containers and their port mappings
- **Docker Compose Integration**: Run Docker Compose projects with automatic port conflict resolution
- **Command Line Interface**: Easy-to-use CLI for both the web interface and Docker Compose commands

## Problem Statement

When running multiple Docker Compose projects simultaneously, port conflicts can occur when different projects attempt to bind to the same host ports. This tool provides an elegant solution by:

1. Detecting port conflicts between Docker Compose projects
2. Automatically remapping ports to avoid conflicts
3. Running Docker Compose with the remapped configuration
4. Providing visibility into container port mappings

## Installation

### Prerequisites

- Docker 20.10+
- Go 1.21+ (for local development)

### Building from Source

```bash
# Clone the repository
git clone https://github.com/alimorgaan/dynamic-port-mapper.git
cd dynamic-port-mapper

# Build the binary
make build
```

## Usage

### Web Interface

Run the application to start the web interface:

```bash
# Run with default configuration (port 5000)
make run

# Or run with a custom port
make run-port
```

Then open your browser to [http://localhost:5000](http://localhost:5000) to view the web interface.

### Command Line Interface

The application provides a command-line interface for Docker Compose integration:

```bash
# General form
./dynamic-port-mapper compose [file] [commands]

# Examples
./dynamic-port-mapper compose docker-compose.yml up -d
./dynamic-port-mapper compose -f custom-compose.yml up
```

### CLI Options

```
Usage:
  dynamic-port-mapper [flags]                    - Run the web interface
  dynamic-port-mapper compose [file] [commands]  - Run a Docker Compose project with automatic port remapping

Flags:
  -port int    Port to run the web server on (default 5000)
  -min  int    Minimum port number for dynamic allocation (default 10000)
  -max  int    Maximum port number for dynamic allocation (default 65000)
```

## How Port Remapping Works

When you run a Docker Compose project with this tool:

1. The tool parses your Docker Compose file to identify all port mappings
2. It checks if any of these ports conflict with:
   - Running Docker containers
   - Other active services on your host machine
3. If conflicts are found, it:
   - Generates a temporary Docker Compose file with remapped ports
   - Shows you what ports were remapped
   - Runs Docker Compose with the modified configuration
4. The web interface then shows all containers with their original and remapped ports

## Development

### Development Environment

The project includes a development environment with hot reloading:

```bash
# Start development environment
make dev

# View logs
make logs-dev

# Stop development environment
make dev-stop
```

### Test Projects

To test port conflict resolution, you can use the included test projects:

```bash
# Start test projects (will have port conflicts)
make test-setup

# Run Dynamic Port Mapper's compose command on a test project
make compose

# Clean up test projects
make test-cleanup
```

## Contributing

Contributions are welcome! Please feel free to submit pull requests.

## License

MIT License

## Running in Production

This project includes an optimized production Docker setup that is secure, lightweight, and efficient.

### Production Docker Setup

The production Docker configuration provides:

- **Security**: Uses distroless base image with minimal attack surface
- **Performance**: Optimized binary with reduced size (-s -w flags)
- **Reliability**: Health checking, automatic restarts, and resource constraints
- **Minimalism**: Only the necessary components are included in the final image

### Running with Docker Compose

The simplest way to run in production mode:

```bash
# Clone the repository
git clone https://github.com/yourusername/dynamic-port-mapper.git
cd dynamic-port-mapper

# Start the container using the convenience script
./prod.sh
```

Or manually with Docker Compose:

```bash
docker-compose up -d
```

The application will be available at http://localhost:5000

### Customizing the Production Setup

You can modify the following settings in docker-compose.yml:

- **Port**: Change the port mapping (default: 5000)
- **Resource limits**: Adjust CPU and memory allocations
- **Timezone**: Set the appropriate TZ environment variable

### Building a Custom Image

To build a custom production image:

```bash
docker build -t dynamic-port-mapper:custom .
```

### Production Considerations

- The container requires access to the Docker socket (`/var/run/docker.sock`) to monitor and manage containers.
- For security, the socket is mounted as read-only.
- The container runs with no new privileges for enhanced security.
- A read-only filesystem is used, with a temporary filesystem mounted at /tmp if needed.
