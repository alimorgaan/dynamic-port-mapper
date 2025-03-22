package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// Container represents a Docker container
type Container struct {
	ID              string
	Image           string
	Command         string
	Created         string
	Status          string
	Ports           string
	Names           string
	ComposeProject  string
	ComposeService  string
	PortMappings    []PortMapping // Detailed port mapping information
	DynamicPorts    bool          // Whether this container has dynamically remapped ports
}

// PortMapping represents a Docker port mapping
type PortMapping struct {
	ContainerPort string
	HostPort      string
	Protocol      string
	OriginalPort  string // The original host port before remapping
}

// Application holds the application state
type Application struct {
	containerStore *ContainerStore
	tmpl           *template.Template
}

// NewApplication creates a new application instance
func NewApplication() (*Application, error) {
	// Initialize container store
	containerStore, err := NewContainerStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize container store: %v", err)
	}

	// Parse HTML template
	tmpl := template.Must(template.New("containers").Parse(`
<!DOCTYPE html>
<html>
<head>
    <title>Dynamic Port Mapper</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            margin: 20px;
            line-height: 1.6;
            background-color: #f9f9f9;
        }
        h1, h2 {
            color: #2c3e50;
            text-align: center;
            margin-bottom: 20px;
            border-bottom: 2px solid #3498db;
            padding-bottom: 10px;
        }
        h2 {
            text-align: left;
            margin-top: 30px;
            font-size: 1.5em;
        }
        table {
            border-collapse: collapse;
            width: 100%;
            margin-top: 20px;
            box-shadow: 0 2px 15px rgba(0,0,0,0.1);
            border-radius: 5px;
            overflow: hidden;
            margin-bottom: 30px;
        }
        th, td {
            text-align: left;
            padding: 12px;
            border-bottom: 1px solid #ddd;
        }
        tr:hover {
            background-color: #f5f5f5;
        }
        th {
            background-color: #3498db;
            color: white;
            font-weight: bold;
        }
        tr:nth-child(even) {
            background-color: #f2f2f2;
        }
        .container-count {
            margin-bottom: 20px;
            font-size: 18px;
            color: #2c3e50;
            text-align: center;
        }
        .error {
            color: #e74c3c;
            font-weight: bold;
            text-align: center;
            padding: 20px;
            background-color: #fadbd8;
            border-radius: 5px;
        }
        .refresh-btn {
            padding: 10px 20px;
            background-color: #2ecc71;
            color: white;
            border: none;
            border-radius: 4px;
            cursor: pointer;
            margin-top: 20px;
            font-size: 16px;
            display: block;
            margin: 20px auto;
            transition: background-color 0.3s;
        }
        .refresh-btn:hover {
            background-color: #27ae60;
        }
        .version-info {
            text-align: center;
            margin-top: 20px;
            font-size: 12px;
            color: #7f8c8d;
        }
        .last-updated {
            text-align: center;
            font-size: 14px;
            color: #7f8c8d;
            margin-top: 5px;
        }
        .project-section {
            background-color: #fff;
            padding: 20px;
            border-radius: 5px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
            margin-bottom: 30px;
        }
        .remapped {
            color: #e67e22;
            font-weight: bold;
        }
        .port-details {
            font-size: 0.85em;
            color: #7f8c8d;
        }
        .port-mapping {
            display: block;
            margin: 5px 0;
        }
        .original-port {
            text-decoration: line-through;
            color: #e74c3c;
            font-size: 0.85em;
        }
    </style>
</head>
<body>
    <h1>Dynamic Port Mapper</h1>
    
    {{if .Error}}
        <p class="error">{{.Error}}</p>
    {{else}}
        <div class="container-count">Total containers: {{len .Containers}}</div>
        <div class="last-updated">Containers are monitored in real-time</div>
        
        {{if .Projects}}
            {{range $project, $containers := .Projects}}
                <div class="project-section">
                    <h2>Project: {{$project}}</h2>
                    <table>
                        <tr>
                            <th>Container</th>
                            <th>Image</th>
                            <th>Service</th>
                            <th>Status</th>
                            <th>Port Mappings</th>
                        </tr>
                        {{range $containers}}
                        <tr>
                            <td>{{.Names}}</td>
                            <td>{{.Image}}</td>
                            <td>{{.ComposeService}}</td>
                            <td>{{.Status}}</td>
                            <td>
                                {{if .PortMappings}}
                                    {{range .PortMappings}}
                                        <span class="port-mapping">
                                            {{if ne .HostPort .OriginalPort}}
                                                <span class="remapped">{{.HostPort}}</span>:<span class="port-details">{{.ContainerPort}}/{{.Protocol}}</span>
                                                <span class="original-port">(was {{.OriginalPort}})</span>
                                            {{else}}
                                                {{.HostPort}}:{{.ContainerPort}}/{{.Protocol}}
                                            {{end}}
                                        </span>
                                    {{end}}
                                {{else}}
                                    {{.Ports}}
                                {{end}}
                            </td>
                        </tr>
                        {{end}}
                    </table>
                </div>
            {{end}}
        {{else}}
            {{if len .Containers}}
                <table>
                    <tr>
                        <th>Container ID</th>
                        <th>Image</th>
                        <th>Command</th>
                        <th>Created</th>
                        <th>Status</th>
                        <th>Ports</th>
                        <th>Names</th>
                    </tr>
                    {{range .Containers}}
                    <tr>
                        <td>{{.ID}}</td>
                        <td>{{.Image}}</td>
                        <td>{{.Command}}</td>
                        <td>{{.Created}}</td>
                        <td>{{.Status}}</td>
                        <td>
                            {{if .PortMappings}}
                                {{range .PortMappings}}
                                    <span class="port-mapping">
                                        {{if ne .HostPort .OriginalPort}}
                                            <span class="remapped">{{.HostPort}}</span>:<span class="port-details">{{.ContainerPort}}/{{.Protocol}}</span>
                                            <span class="original-port">(was {{.OriginalPort}})</span>
                                        {{else}}
                                            {{.HostPort}}:{{.ContainerPort}}/{{.Protocol}}
                                        {{end}}
                                    </span>
                                {{end}}
                            {{else}}
                                {{.Ports}}
                            {{end}}
                        </td>
                        <td>{{.Names}}</td>
                    </tr>
                    {{end}}
                </table>
            {{else}}
                <p>No containers are currently running.</p>
            {{end}}
        {{end}}
    {{end}}
    <button class="refresh-btn" onclick="location.reload()">Refresh</button>
    <div class="version-info">Dynamic Port Mapper v1.0.0 - Automatically resolves port conflicts for Docker Compose projects</div>
</body>
</html>
`))

	return &Application{
		containerStore: containerStore,
		tmpl:           tmpl,
	}, nil
}

// indexHandler handles requests to the root path
func (app *Application) indexHandler(w http.ResponseWriter, r *http.Request) {
	// Only respond to the root path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Get containers from the store
	containers := app.containerStore.GetContainers()
	
	// Get containers organized by project
	projects := app.containerStore.GetContainersByComposeProject()

	// Prepare template data
	data := struct {
		Containers []Container
		Projects   map[string][]Container
		Error      string
	}{
		Containers: containers,
		Projects:   projects,
	}

	// Render template
	w.Header().Set("Content-Type", "text/html")
	if err := app.tmpl.Execute(w, data); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// Close gracefully shuts down the application
func (app *Application) Close() {
	app.containerStore.Close()
}

// runComposeCommand runs a Docker Compose project with dynamically allocated ports
func runComposeCommand(containerStore *ContainerStore, composeFile string, args []string) error {
	log.Printf("Checking for port conflicts in Compose file: %s", composeFile)
	
	// Check for port conflicts
	remappings, err := containerStore.CheckComposePortConflicts(composeFile)
	if err != nil {
		return fmt.Errorf("failed to check for port conflicts: %v", err)
	}
	
	// If there are no conflicts, run the compose command directly
	if len(remappings) == 0 {
		log.Println("No port conflicts detected, running docker-compose directly")
		
		cmd := exec.Command("docker-compose", append([]string{"-f", composeFile}, args...)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	
	// Generate a new compose file with remapped ports
	log.Printf("Found %d port conflicts, generating remapped compose file", len(remappings))
	remappedFile, err := containerStore.GenerateRemappedComposeFile(composeFile, remappings)
	if err != nil {
		return fmt.Errorf("failed to generate remapped compose file: %v", err)
	}
	defer os.Remove(remappedFile) // Clean up the temporary file
	
	// Print the remappings for the user
	log.Println("Port remappings:")
	for servicePort, newPort := range remappings {
		parts := strings.Split(servicePort, ":")
		if len(parts) == 2 {
			log.Printf("  %s: %s -> %s", parts[0], parts[1], newPort)
		}
	}
	
	// Run docker-compose with the new file
	log.Printf("Running docker-compose with remapped ports")
	cmd := exec.Command("docker-compose", append([]string{"-f", remappedFile}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// printUsage prints the usage instructions
func printUsage() {
	fmt.Println("Dynamic Port Mapper for Docker")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  dynamic-port-mapper [flags]                    - Run the web interface")
	fmt.Println("  dynamic-port-mapper compose [file] [commands]  - Run a Docker Compose project with automatic port remapping")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -port int    Port to run the web server on (default 5000)")
	fmt.Println("  -min  int    Minimum port number for dynamic allocation (default 10000)")
	fmt.Println("  -max  int    Maximum port number for dynamic allocation (default 65000)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  dynamic-port-mapper")
	fmt.Println("  dynamic-port-mapper -port 8080")
	fmt.Println("  dynamic-port-mapper compose docker-compose.yml up -d")
	fmt.Println("  dynamic-port-mapper compose -f custom-compose.yml up")
}

func main() {
	// Define command line flags
	port := flag.Int("port", 5000, "Port to run the web server on")
	minPort := flag.Int("min", 10000, "Minimum port number for dynamic allocation")
	maxPort := flag.Int("max", 65000, "Maximum port number for dynamic allocation")
	help := flag.Bool("help", false, "Show help")
	
	// Parse flags
	flag.Parse()
	
	// Show help if requested
	if *help {
		printUsage()
		return
	}
	
	// Initialize the container store
	containerStore, err := NewContainerStore()
	if err != nil {
		log.Fatalf("Failed to initialize container store: %v", err)
	}
	
	// Set port range
	containerStore.portRangeMin = *minPort
	containerStore.portRangeMax = *maxPort
	
	// Check if we're running a docker-compose command
	args := flag.Args()
	if len(args) > 0 && args[0] == "compose" {
		// We're running in docker-compose mode
		if len(args) < 2 {
			log.Fatal("Error: Missing compose file. Usage: dynamic-port-mapper compose [file] [commands]")
		}
		
		// Check if we have a -f flag
		composeFile := args[1]
		composeArgs := args[2:]
		
		// If the first compose arg is -f, use the next arg as the file
		if composeFile == "-f" && len(args) >= 3 {
			composeFile = args[2]
			composeArgs = args[3:]
		}
		
		// Make sure the compose file exists
		if _, err := os.Stat(composeFile); os.IsNotExist(err) {
			log.Fatalf("Error: Compose file not found: %s", composeFile)
		}
		
		// Run the compose command
		if err := runComposeCommand(containerStore, composeFile, composeArgs); err != nil {
			log.Fatalf("Error running docker-compose: %v", err)
		}
		
		return
	}
	
	// Otherwise, we're running the web server
	app, err := NewApplication()
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Close()

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, gracefully shutting down...")
		app.Close()
		os.Exit(0)
	}()

	// Register our handler
	http.HandleFunc("/", app.indexHandler)

	// Start the server
	serverAddr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting Dynamic Port Mapper on port %d...", *port)
	log.Printf("Open http://localhost:%d in your browser to view running Docker containers with remapped ports", *port)
	log.Printf("Port range for dynamic allocation: %d-%d", containerStore.portRangeMin, containerStore.portRangeMax)
	log.Printf("To run a Docker Compose project with automatic port remapping, use: dynamic-port-mapper compose [file] [commands]")
	if err := http.ListenAndServe(serverAddr, nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
} 