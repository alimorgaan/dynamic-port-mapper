# Dynamic Port Mapper for Docker

## Overview

Dynamic Port Mapper solves the common problem of port conflicts when running multiple Docker Compose projects simultaneously. Instead of manually editing docker-compose files to resolve port conflicts, this tool automatically detects and resolves conflicts by dynamically remapping ports.

## Problem Solved

When multiple Docker Compose projects try to use the same host ports:

- Without this tool: You need to manually edit each docker-compose.yml file to use different ports
- With Dynamic Port Mapper: Ports are automatically remapped to avoid conflicts, and containers are restarted with the new configuration

## Quick Start

```bash
# Run with Docker Compose
docker compose up --build -d

# Access the web interface
open http://localhost:5000
```

## How to Use

1. Start the Dynamic Port Mapper:

   ```bash
   docker compose up --build -d
   ```

2. Access the web interface at http://localhost:5000 to view all running containers and their port mappings

3. Run your Docker Compose projects as usual:

   ```bash
   # Example: Try running these sample projects with conflicting ports
   cd test-projects/app1
   docker compose up -d

   cd ../app2
   docker compose up -d
   ```

4. Dynamic Port Mapper will automatically:
   - Detect port conflicts
   - Allocate new ports
   - Restart containers with resolved configurations
   - Display the new port mappings in the web interface

## Features

- **Real-time Container Monitoring**: View all Docker containers and their port mappings
- **Automatic Port Conflict Resolution**: No need to manually edit docker-compose files
- **Web Interface**: Clean UI showing containers and their original/remapped ports
- **Minimal Setup**: Just run it and forget about port conflicts

## Technical Details

- Port range for dynamic allocation: 10000-65000 (configurable)
- Container restart occurs only when port conflicts are detected
- All changes are visible through the web interface
- No modification of your original docker-compose files

## Running in Production

The provided `prod.sh` script makes it easy to run in production:

```bash
./prod.sh
```

## License

MIT License
