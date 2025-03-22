package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// DockerEvent represents a Docker event
type DockerEvent struct {
	Status    string
	ID        string
	From      string
	Type      string
	Action    string
	Actor     DockerActor `json:"Actor"`
	TimeNano  int64       `json:"timeNano"`
	Scope     string      `json:"scope"`
	Time      int64       `json:"time"`
}

// DockerActor contains information about the actor causing the event
type DockerActor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

// ContainerStore maintains the state of all containers and port mappings
type ContainerStore struct {
	containers           map[string]Container
	portMappings         map[string]map[string]string // containerID -> containerPort:hostPort mapping
	processedContainers  map[string]bool              // In-memory tracking of containers with dynamic ports
	mu                   sync.RWMutex
	eventCmd             *exec.Cmd
	done                 chan struct{}
	portRangeMin         int
	portRangeMax         int
}

// portRegex matches port mappings in the format [IP:]PORT->PORT/PROTO
var portRegex = regexp.MustCompile(`(?:(\d+\.\d+\.\d+\.\d+):)?(\d+)->(\d+)\/(\w+)`)

// NewContainerStore creates a new container store
func NewContainerStore() (*ContainerStore, error) {
	// Seed the random number generator for port allocation
	rand.Seed(time.Now().UnixNano())

	store := &ContainerStore{
		containers:          make(map[string]Container),
		portMappings:        make(map[string]map[string]string),
		processedContainers: make(map[string]bool),
		done:                make(chan struct{}),
		portRangeMin:        10000,  // Default port range
		portRangeMax:        65000,
	}

	// Initialize the container list
	if err := store.refreshContainers(); err != nil {
		return nil, err
	}

	// Start listening for Docker events
	go store.listenForEvents()

	return store, nil
}

// refreshContainers loads all current containers from Docker
func (s *ContainerStore) refreshContainers() error {
	// List all running containers using docker ps with additional name and label info
	cmd := exec.Command("docker", "ps", "--format", "{{json .}}", "--no-trunc")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error listing containers: %v", err)
	}

	// Store current state of port mappings before lock
	s.mu.RLock()
	currentPortMappings := make(map[string]map[string]string)
	for id, mappings := range s.portMappings {
		currentPortMappings[id] = make(map[string]string)
		for k, v := range mappings {
			currentPortMappings[id][k] = v
		}
	}
	// Also store processed containers
	processedContainers := make(map[string]bool)
	for id, processed := range s.processedContainers {
		processedContainers[id] = processed
	}
	s.mu.RUnlock()

	// Create temporary structures to hold the new state
	newContainers := make(map[string]Container)
	newPortMappings := make(map[string]map[string]string)
	newProcessedContainers := make(map[string]bool)

	// Parse the output
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		var dockerContainer struct {
			ID         string `json:"ID"`
			Image      string `json:"Image"`
			Command    string `json:"Command"`
			RunningFor string `json:"RunningFor"`
			Status     string `json:"Status"`
			Ports      string `json:"Ports"`
			Names      string `json:"Names"`
		}

		if err := json.Unmarshal([]byte(line), &dockerContainer); err != nil {
			log.Printf("Error parsing container JSON: %v", err)
			continue
		}

		// Make sure we can still look up this container before proceeding
		// Sometimes Docker CLI output can lag behind actual state
		checkCmd := exec.Command("docker", "inspect", "--format", "{{.ID}}", dockerContainer.ID)
		if err := checkCmd.Run(); err != nil {
			log.Printf("Container %s appears to no longer exist, skipping", dockerContainer.ID)
			continue
		}

		// Look up the compose project and service labels directly
		composeProject := extractLabel(dockerContainer.ID, "com.docker.compose.project")
		composeService := extractLabel(dockerContainer.ID, "com.docker.compose.service")
		
		// Try additional labels if the standard ones don't work
		if composeProject == "" {
			// Try alternative label names that might be used
			alternativeLabels := []string{
				"docker-compose.project",
				"io.compose.project",
				"com.docker.project",
				"project",
			}
			
			for _, altLabel := range alternativeLabels {
				composeProject = extractLabel(dockerContainer.ID, altLabel)
				if composeProject != "" {
					log.Printf("Found compose project '%s' for container %s using alternative label: %s", 
						composeProject, dockerContainer.Names, altLabel)
					break
				}
			}
			
			// If still no project but we have a compose service, use the container's name to infer project
			if composeProject == "" {
				// Extract project name from container name
				containerName := dockerContainer.Names
				// Remove any leading slash
				containerName = strings.TrimPrefix(containerName, "/")
				
				// Many compose-created containers follow naming patterns:
				// 1. project_service_1 (most common)
				// 2. project-service-1
				// Try to extract project name
				
				// First try underscore pattern
				parts := strings.Split(containerName, "_")
				if len(parts) >= 2 && parts[0] != "" {
					composeProject = parts[0]
					log.Printf("Inferred compose project '%s' from container name (underscore pattern): %s", 
						composeProject, containerName)
				} else {
					// Try hyphen pattern
					parts = strings.Split(containerName, "-")
					if len(parts) >= 3 && parts[0] != "" {
						// In "project-service-1" pattern, first part is project
						composeProject = parts[0]
						log.Printf("Inferred compose project '%s' from container name (hyphen pattern): %s", 
							composeProject, containerName)
					}
				}
			}
		}
		
		// If still no service name but there's a project, try to infer the service 
		if composeService == "" && composeProject != "" {
			// Try alternative service labels
			alternativeLabels := []string{
				"docker-compose.service",
				"io.compose.service",
				"com.docker.service",
				"service",
			}
			
			for _, altLabel := range alternativeLabels {
				composeService = extractLabel(dockerContainer.ID, altLabel)
				if composeService != "" {
					break
				}
			}
			
			// If still no service but we have a project, infer from name
			if composeService == "" {
				// Extract from name pattern like project_service_1
				parts := strings.Split(dockerContainer.Names, "_")
				if len(parts) >= 2 {
					// Service is often the middle part
					composeService = parts[1]
				}
			}
		}

		// Convert to our Container type
		container := Container{
			ID:             dockerContainer.ID,
			Image:          dockerContainer.Image,
			Command:        dockerContainer.Command,
			Created:        dockerContainer.RunningFor,
			Status:         dockerContainer.Status,
			Ports:          dockerContainer.Ports,
			Names:          dockerContainer.Names,
			ComposeProject: composeProject,
			ComposeService: composeService,
			PortMappings:   []PortMapping{},
			DynamicPorts:   false,
		}

		// First just parse the port mappings without remapping
		// If remapping is needed, we'll collect them to handle after releasing the lock
		container.PortMappings, container.DynamicPorts = s.parsePortsWithoutRemapping(dockerContainer.ID, dockerContainer.Ports, currentPortMappings)

		// Store container
		newContainers[dockerContainer.ID] = container

		// Store port mappings
		if len(container.PortMappings) > 0 {
			mappings := make(map[string]string)
			for _, pm := range container.PortMappings {
				key := fmt.Sprintf("%s/%s", pm.ContainerPort, pm.Protocol)
				mappings[key] = pm.HostPort
			}
			newPortMappings[dockerContainer.ID] = mappings
		}

		// Keep track of processed containers
		if processed, exists := processedContainers[dockerContainer.ID]; exists && processed {
			newProcessedContainers[dockerContainer.ID] = true
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error scanning docker ps output: %v", err)
	}

	// Now update the state atomically with a single lock
	s.mu.Lock()
	s.containers = newContainers
	s.portMappings = newPortMappings
	s.processedContainers = newProcessedContainers
	s.mu.Unlock()

	return nil
}

// parsePortsWithoutRemapping parses port mappings without doing any remapping
func (s *ContainerStore) parsePortsWithoutRemapping(containerID, portsStr string, existingMappings map[string]map[string]string) ([]PortMapping, bool) {
	// If container already had mappings, restore them
	if mappings, exists := existingMappings[containerID]; exists {
		return s.restorePortMappings(portsStr, mappings)
	}

	var mappings []PortMapping
	dynamicPorts := false

	// Check if this container has already been processed
	processed := s.isContainerProcessed(containerID)
	
	// Parse the ports without remapping
	matches := portRegex.FindAllStringSubmatch(portsStr, -1)
	for _, match := range matches {
		hostPort := match[2]
		containerPort := match[3]
		protocol := match[4]
		mappings = append(mappings, PortMapping{
			ContainerPort: containerPort,
			HostPort:      hostPort,
			Protocol:      protocol,
			OriginalPort:  hostPort, // Since we're not remapping, original = current
		})
	}
	
	// If container is already processed, just return the mappings
	if processed {
		return mappings, true
	}
	
	// Mark container as processed if all its ports are in our dynamic range
	allPortsInDynamicRange := true
	if len(matches) > 0 {
		for _, match := range matches {
			hostPort := match[2]
			portInt, err := strconv.Atoi(hostPort)
			if err != nil || portInt < s.portRangeMin || portInt > s.portRangeMax {
				allPortsInDynamicRange = false
				break
			}
		}
		
		if allPortsInDynamicRange {
			dynamicPorts = true
			// Add to our processed tracking to avoid future rechecks
			s.addDynamicPortLabel(containerID)
		}
	}
	
	return mappings, dynamicPorts
}

// extractLabel retrieves a specific Docker label from a container
func extractLabel(containerID string, label string) string {
	// First try the more specific format template
	cmd := exec.Command("docker", "inspect", "--format", fmt.Sprintf("{{index .Config.Labels \"%s\"}}", label), containerID)
	output, err := cmd.Output()
	if err == nil {
		// Trim whitespace and check for empty string
		value := strings.TrimSpace(string(output))
		if value != "<no value>" && value != "" {
			return value
		}
	}
	
	// Try the alternate format as a fallback
	cmd = exec.Command("docker", "inspect", "--format", fmt.Sprintf("{{.Config.Labels.%s}}", label), containerID)
	output, err = cmd.Output()
	if err != nil {
		return ""
	}
	
	// Trim whitespace and check for empty string
	value := strings.TrimSpace(string(output))
	if value == "<no value>" || value == "" {
		return ""
	}
	return value
}

// parsePortMappings extracts port mapping details from the port string
// It also handles remapping ports for containers that need dynamic port allocation
func (s *ContainerStore) parsePortMappings(containerID, portsStr string, existingMappings map[string]map[string]string) ([]PortMapping, bool) {
	// If container already had mappings, restore them
	if mappings, exists := existingMappings[containerID]; exists {
		return s.restorePortMappings(portsStr, mappings)
	}

	var mappings []PortMapping
	dynamicPorts := false

	// Check if this container has already been processed
	if s.isContainerProcessed(containerID) {
		log.Printf("Container %s has already been processed, skipping port remapping", containerID)
		// Parse the ports without remapping
		matches := portRegex.FindAllStringSubmatch(portsStr, -1)
		for _, match := range matches {
			hostPort := match[2]
			containerPort := match[3]
			protocol := match[4]
			mappings = append(mappings, PortMapping{
				ContainerPort: containerPort,
				HostPort:      hostPort,
				Protocol:      protocol,
				OriginalPort:  hostPort, // Since we're not remapping, original = current
			})
		}
		return mappings, true
	}

	// Check if this is a port in our dynamic range already
	// If so, it was likely already assigned by us during a previous run
	matches := portRegex.FindAllStringSubmatch(portsStr, -1)
	allPortsInDynamicRange := true
	
	for _, match := range matches {
		hostPort := match[2]
		portInt, err := strconv.Atoi(hostPort)
		if err != nil || portInt < s.portRangeMin || portInt > s.portRangeMax {
			allPortsInDynamicRange = false
			break
		}
	}
	
	// If all ports are already in our dynamic range and the container is running,
	// consider them already remapped
	if allPortsInDynamicRange && len(matches) > 0 {
		log.Printf("Container %s has all ports in dynamic range, considering already processed", containerID)
		// Parse the ports without remapping but mark as dynamic
		for _, match := range matches {
			hostPort := match[2]
			containerPort := match[3]
			protocol := match[4]
			mappings = append(mappings, PortMapping{
				ContainerPort: containerPort,
				HostPort:      hostPort,
				Protocol:      protocol,
				OriginalPort:  hostPort, // We don't know the original, so use current
			})
		}
		
		// Add to our processed tracking to avoid future rechecks
		s.addDynamicPortLabel(containerID)
		
		return mappings, true
	}

	// First, parse the port string to extract all mappings
	for _, match := range matches {
		hostPort := match[2]
		containerPort := match[3]
		protocol := match[4]
		originalPort := hostPort

		// Always check if a dynamic port remap is needed
		needsRemap, newPort := s.checkPortCollision(containerID, hostPort, protocol)
		if needsRemap {
			// Remember that we changed this port from its original value
			dynamicPorts = true
			
			// Execute the port remap
			if err := s.remapContainerPort(containerID, hostPort, newPort, containerPort, protocol); err != nil {
				log.Printf("Failed to remap port %s->%s for container %s: %v", 
					hostPort, newPort, containerID, err)
				continue
			}
			
			// Update the host port to the new one
			hostPort = newPort
		}

		mappings = append(mappings, PortMapping{
			ContainerPort: containerPort,
			HostPort:      hostPort,
			Protocol:      protocol,
			OriginalPort:  originalPort,
		})
	}

	// If we've remapped ports, add a label to the container to mark it as processed
	if dynamicPorts {
		s.addDynamicPortLabel(containerID)
	}

	return mappings, dynamicPorts
}

// addDynamicPortLabel adds a label to the container indicating it has dynamically assigned ports
func (s *ContainerStore) addDynamicPortLabel(containerID string) {
	// First record it in our in-memory tracking map
	s.mu.Lock()
	s.processedContainers[containerID] = true
	s.mu.Unlock()
	
	log.Printf("Added container %s to in-memory tracking of processed containers", containerID)
	
	// Still try to add the Docker label as a backup, but don't rely on it
	// First, check if the container still exists before trying to add a label
	checkCmd := exec.Command("docker", "inspect", "--format", "{{.ID}}", containerID)
	if err := checkCmd.Run(); err != nil {
		log.Printf("Container %s no longer exists, can't add label", containerID)
		return
	}
	
	// Try to add the label in a more reliable way using docker container update
	cmd := exec.Command("docker", "container", "update", "--label", "com.dynamic-port-mapper.has-dynamic-ports=true", containerID)
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to add dynamic port label to container %s via update: %v", containerID, err)
		
		// As a fallback, try the original method
		fallbackCmd := exec.Command("docker", "container", "label", containerID, "com.dynamic-port-mapper.has-dynamic-ports=true")
		if err := fallbackCmd.Run(); err != nil {
			log.Printf("Failed to add dynamic port label to container %s via label: %v", containerID, err)
			// If both methods fail, we'll rely on our in-memory tracking
		} else {
			log.Printf("Added dynamic port label to container %s via label command", containerID)
		}
	} else {
		log.Printf("Added dynamic port label to container %s via update command", containerID)
	}
}

// isContainerProcessed checks if a container has already been processed by checking
// both in-memory tracking and Docker labels
func (s *ContainerStore) isContainerProcessed(containerID string) bool {
	// First check our in-memory tracking
	s.mu.RLock()
	processed, exists := s.processedContainers[containerID]
	s.mu.RUnlock()
	
	if exists && processed {
		return true
	}
	
	// As a fallback, check the Docker label
	hasDynamicPorts := extractLabel(containerID, "com.dynamic-port-mapper.has-dynamic-ports")
	if hasDynamicPorts == "true" {
		// Add to our in-memory tracking for future checks
		s.mu.Lock()
		s.processedContainers[containerID] = true
		s.mu.Unlock()
		return true
	}
	
	return false
}

// restorePortMappings recreates the port mappings from previously stored data
func (s *ContainerStore) restorePortMappings(portsStr string, storedMappings map[string]string) ([]PortMapping, bool) {
	var mappings []PortMapping
	dynamicPorts := false

	// Parse the raw port string
	matches := portRegex.FindAllStringSubmatch(portsStr, -1)
	for _, match := range matches {
		originalHostPort := match[2]
		containerPort := match[3]
		protocol := match[4]

		// Look up if we have a stored mapping for this port
		key := fmt.Sprintf("%s/%s", containerPort, protocol)
		if storedPort, exists := storedMappings[key]; exists && storedPort != originalHostPort {
			// We had previously remapped this port
			dynamicPorts = true
			mappings = append(mappings, PortMapping{
				ContainerPort: containerPort,
				HostPort:      storedPort,
				Protocol:      protocol,
				OriginalPort:  originalHostPort,
			})
		} else {
			// This port wasn't remapped
			mappings = append(mappings, PortMapping{
				ContainerPort: containerPort,
				HostPort:      originalHostPort,
				Protocol:      protocol,
				OriginalPort:  originalHostPort,
			})
		}
	}

	return mappings, dynamicPorts
}

// checkPortCollision determines if a port needs to be remapped
func (s *ContainerStore) checkPortCollision(containerID, hostPort, protocol string) (bool, string) {
	portInt, err := strconv.Atoi(hostPort)
	if err != nil {
		log.Printf("Invalid port number: %s", hostPort)
		return false, hostPort
	}

	// Check if the port is already in our managed port range
	// If it is, this likely means it was previously dynamically allocated by us
	// So we don't need to remap it again
	if portInt >= s.portRangeMin && portInt <= s.portRangeMax {
		// Check if the port is already in use by another container
		if s.isPortUsedByOtherContainer(containerID, portInt, protocol) {
			// Only in this case do we need to remap it
			newPort := s.allocateRandomPort()
			log.Printf("Port %s is in our dynamic range but used by another container, remapping to %d", 
				hostPort, newPort)
			return true, strconv.Itoa(newPort)
		}
		
		// If the port is in our range and not used by another container, keep using it
		log.Printf("Port %s is in our dynamic range and available, no need to remap", hostPort)
		return false, hostPort
	}

	// Port is outside our managed range - always remap it to our dynamic range
	newPort := s.allocateRandomPort()
	log.Printf("Port %s is outside our dynamic range (%d-%d), automatically remapping to %d", 
		hostPort, s.portRangeMin, s.portRangeMax, newPort)
	return true, strconv.Itoa(newPort)
}

// isPortUsedByOtherContainer checks if a port is used by a container other than the specified one
func (s *ContainerStore) isPortUsedByOtherContainer(containerID string, port int, protocol string) bool {
	for id, container := range s.containers {
		if id == containerID {
			continue // Skip the container we're checking for
		}
		
		for _, mapping := range container.PortMappings {
			existingPort, _ := strconv.Atoi(mapping.HostPort)
			if existingPort == port && mapping.Protocol == protocol {
				return true // Port is used by another container
			}
		}
	}
	
	return false
}

// allocateRandomPort finds a free port in the configured range
func (s *ContainerStore) allocateRandomPort() int {
	for i := 0; i < 100; i++ { // Try up to 100 times to find an available port
		port := rand.Intn(s.portRangeMax-s.portRangeMin) + s.portRangeMin
		
		// Check if port is available
		if s.isPortAvailable(port) {
			return port
		}
	}

	// If we couldn't find a port, return a random one as a fallback
	return rand.Intn(s.portRangeMax-s.portRangeMin) + s.portRangeMin
}

// isPortAvailable checks if a port is available on the host
func (s *ContainerStore) isPortAvailable(port int) bool {
	// First check if any of our tracked containers are using this port
	for _, container := range s.containers {
		for _, mapping := range container.PortMappings {
			existingPort, _ := strconv.Atoi(mapping.HostPort)
			if existingPort == port {
				return false
			}
		}
	}

	// Then check if the port is actually available on the host
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// remapContainerPort changes a container's port mapping
func (s *ContainerStore) remapContainerPort(containerID, oldHostPort, newHostPort, containerPort, protocol string) error {
	log.Printf("Remapping port for container %s: %s->%s:%s/%s", 
		containerID, oldHostPort, newHostPort, containerPort, protocol)
	
	// Mark this container as processed before we do anything
	// This way, even if something fails during the remap process,
	// we won't get into an infinite restart loop
	s.addDynamicPortLabel(containerID)
	
	// 1. Inspect the container to get its configuration
	inspectCmd := exec.Command("docker", "inspect", containerID)
	inspectOutput, err := inspectCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to inspect container %s: %v", containerID, err)
	}
	
	var containerData []map[string]interface{}
	if err := json.Unmarshal(inspectOutput, &containerData); err != nil {
		return fmt.Errorf("failed to parse container inspection data: %v", err)
	}
	
	if len(containerData) == 0 {
		return fmt.Errorf("no inspection data found for container %s", containerID)
	}
	
	// Extract container information
	containerInfo := containerData[0]
	
	// 2. Extract essential information from inspection data
	containerName := containerInfo["Name"].(string)
	if containerName[0] == '/' {
		containerName = containerName[1:] // Remove leading slash
	}

	// Check if this is a Docker Compose container
	composeProject := extractLabel(containerID, "com.docker.compose.project")
	if composeProject != "" {
		log.Printf("Container %s belongs to Compose project %s - consider using docker-compose to manage it", 
			containerID, composeProject)
	}
	
	// Get image
	image := containerInfo["Config"].(map[string]interface{})["Image"].(string)
	
	// Get network mode
	hostConfig := containerInfo["HostConfig"].(map[string]interface{})
	networkMode := hostConfig["NetworkMode"].(string)
	
	// Get environment variables
	env := containerInfo["Config"].(map[string]interface{})["Env"].([]interface{})
	envVars := make([]string, len(env))
	for i, e := range env {
		envVars[i] = e.(string)
	}
	
	// Get volumes
	var volumeArgs []string
	if mounts, ok := containerInfo["Mounts"].([]interface{}); ok {
		for _, m := range mounts {
			mount := m.(map[string]interface{})
			src := mount["Source"].(string)
			dst := mount["Destination"].(string)
			volumeArgs = append(volumeArgs, "-v", fmt.Sprintf("%s:%s", src, dst))
		}
	}
	
	// Get existing port bindings
	portBindings := make(map[string][]map[string]string)
	if pb, ok := hostConfig["PortBindings"].(map[string]interface{}); ok {
		for port, bindings := range pb {
			if port == fmt.Sprintf("%s/%s", containerPort, protocol) {
				// This is the port we're remapping, skip it
				continue
			}
			
			bindingsArray := bindings.([]interface{})
			portBindings[port] = make([]map[string]string, len(bindingsArray))
			
			for i, b := range bindingsArray {
				binding := b.(map[string]interface{})
				hostIP := ""
				if ip, ok := binding["HostIp"]; ok {
					hostIP = ip.(string)
				}
				hostPort := ""
				if hp, ok := binding["HostPort"]; ok {
					hostPort = hp.(string)
				}
				
				portBindings[port][i] = map[string]string{
					"HostIp":   hostIP,
					"HostPort": hostPort,
				}
			}
		}
	}
	
	// Add our new port mapping
	portToRemap := fmt.Sprintf("%s/%s", containerPort, protocol)
	portBindings[portToRemap] = []map[string]string{
		{
			"HostIp":   "",  // Default to all interfaces
			"HostPort": newHostPort,
		},
	}
	
	// Get restart policy
	restartPolicy := ""
	if policy, ok := hostConfig["RestartPolicy"].(map[string]interface{}); ok {
		if name, ok := policy["Name"].(string); ok && name != "" {
			restartPolicy = name
			if name == "on-failure" {
				if maxRetry, ok := policy["MaximumRetryCount"].(float64); ok {
					restartPolicy = fmt.Sprintf("%s:%d", restartPolicy, int(maxRetry))
				}
			}
		}
	}
	
	// Get labels
	labels := containerInfo["Config"].(map[string]interface{})["Labels"].(map[string]interface{})
	
	// Add our dynamic port mapper label to indicate this container has been processed
	labels["com.dynamic-port-mapper.has-dynamic-ports"] = "true"
	
	labelArgs := []string{}
	for k, v := range labels {
		labelArgs = append(labelArgs, "--label", fmt.Sprintf("%s=%s", k, v.(string)))
	}

	// 3. Stop the container, with a timeout to ensure it stops gracefully
	log.Printf("Stopping container %s to remap ports", containerID)
	stopCmd := exec.Command("docker", "stop", "--time", "10", containerID)
	if err := stopCmd.Run(); err != nil {
		log.Printf("Warning: Failed to stop container %s gracefully: %v", containerID, err)
		// Try to kill it forcefully if stop failed
		killCmd := exec.Command("docker", "kill", containerID)
		if err := killCmd.Run(); err != nil {
			return fmt.Errorf("failed to stop/kill container %s: %v", containerID, err)
		}
	}
	
	// Wait a bit to ensure everything is settled
	time.Sleep(1 * time.Second)
	
	// 4. Remove the container but keep its volumes
	log.Printf("Removing container %s to recreate with new port mapping", containerID)
	removeCmd := exec.Command("docker", "rm", containerID)
	if err := removeCmd.Run(); err != nil {
		return fmt.Errorf("failed to remove container %s: %v", containerID, err)
	}
	
	// 5. Reconstruct the docker run command with the new port mapping
	createArgs := []string{"run", "-d"}
	
	// Add name
	createArgs = append(createArgs, "--name", containerName)
	
	// Add network mode
	if networkMode != "" && networkMode != "default" {
		createArgs = append(createArgs, "--network", networkMode)
	}
	
	// Add restart policy
	if restartPolicy != "" {
		createArgs = append(createArgs, "--restart", restartPolicy)
	}
	
	// Add volume mounts
	createArgs = append(createArgs, volumeArgs...)
	
	// Add port mappings
	for port, bindings := range portBindings {
		for _, binding := range bindings {
			hostPort := binding["HostPort"]
			hostIP := binding["HostIp"]
			
			if hostIP != "" && hostIP != "0.0.0.0" {
				createArgs = append(createArgs, "-p", fmt.Sprintf("%s:%s:%s", hostIP, hostPort, port))
			} else {
				createArgs = append(createArgs, "-p", fmt.Sprintf("%s:%s", hostPort, port))
			}
		}
	}
	
	// Add environment variables
	for _, env := range envVars {
		createArgs = append(createArgs, "-e", env)
	}
	
	// Add labels
	createArgs = append(createArgs, labelArgs...)
	
	// Finally, add the image name
	createArgs = append(createArgs, image)
	
	// 6. Create and start the new container
	log.Printf("Creating new container with remapped port: %s -> %s", oldHostPort, newHostPort)
	log.Printf("Running: docker %s", strings.Join(createArgs, " "))
	
	createCmd := exec.Command("docker", createArgs...)
	createOutput, err := createCmd.CombinedOutput()
	if err != nil {
		log.Printf("Command failed: docker %s", strings.Join(createArgs, " "))
		return fmt.Errorf("failed to create new container with remapped port: %v, output: %s", 
			err, string(createOutput))
	}
	
	// Get the new container ID from the output
	newContainerID := strings.TrimSpace(string(createOutput))
	log.Printf("Successfully remapped port for container %s (new ID: %s): %s -> %s", 
		containerID, newContainerID, oldHostPort, newHostPort)
	
	// Make sure to mark the new container as processed right away
	s.mu.Lock()
	s.processedContainers[newContainerID] = true
	s.mu.Unlock()
	
	// 7. Wait a bit for the container to start
	time.Sleep(1 * time.Second)
	
	return nil
}

// listenForEvents starts listening for Docker events
func (s *ContainerStore) listenForEvents() {
	// Use docker events command to listen for events
	s.eventCmd = exec.Command("docker", "events", "--format", "{{json .}}", "--filter", "type=container")
	
	stdout, err := s.eventCmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating pipe for docker events: %v", err)
		return
	}

	if err := s.eventCmd.Start(); err != nil {
		log.Printf("Error starting docker events: %v", err)
		return
	}

	// Process events
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in processEvents: %v", r)
				// Try to restart the events listener
				time.Sleep(2 * time.Second)
				go s.listenForEvents()
			}
		}()
		s.processEvents(stdout)
	}()

	// Wait for command to finish and restart if needed
	go func() {
		err := s.eventCmd.Wait()
		log.Printf("Docker events command exited: %v", err)
		
		select {
		case <-s.done:
			// We're shutting down, don't restart
			return
		default:
			// Command exited unexpectedly, restart after a delay
			time.Sleep(5 * time.Second)
			go s.listenForEvents()
		}
	}()
}

// processEvents reads and processes Docker events
func (s *ContainerStore) processEvents(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		var event DockerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("Error parsing event JSON: %v", err)
			continue
		}

		// Only handle container events
		if event.Type != "container" {
			continue
		}

		// Don't hold the lock during this whole function
		// Handle specific container actions for port remapping
		switch event.Status {
		case "start":
			// Handle container start in a separate goroutine
			go s.handleContainerStart(event.ID)
			
		case "die", "stop", "kill", "destroy", "remove":
			// Handle in a separate goroutine to avoid blocking
			go s.handleContainerStop(event.ID)
			
		case "exec_create", "exec_start", "exec_die":
			// Ignore exec events
			continue
			
		default:
			// For any other events, log them and trigger a refresh to ensure state consistency
			log.Printf("Received container event: %s for container %s", event.Status, event.ID)
			go func() {
				// Small delay to allow Docker state to settle
				time.Sleep(300 * time.Millisecond)
				s.refreshContainers()
			}()
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error scanning docker events: %v", err)
	}
}

// handleContainerStart processes a container start event
func (s *ContainerStore) handleContainerStart(containerID string) {
	// Add a small delay to ensure docker has fully initialized the container
	time.Sleep(500 * time.Millisecond)

	log.Printf("Container started: %s", containerID)
	
	// Check if this is a Docker Compose container
	composeProject := extractLabel(containerID, "com.docker.compose.project")
	if composeProject != "" {
		log.Printf("Container belongs to Compose project: %s", composeProject)
	}
	
	// Check if this container has already been processed by our tool
	if s.isContainerProcessed(containerID) {
		log.Printf("Container %s already has dynamic ports assigned, skipping port remapping", containerID)
		// Just refresh the containers to update our state
		if err := s.refreshContainers(); err != nil {
			log.Printf("Error refreshing containers: %v", err)
		}
		return
	}
	
	// The container is new and not yet processed - check for port conflicts
	// This is important for containers started directly by Docker Compose
	// that weren't initially started via our tool
	
	// Get container details
	cmd := exec.Command("docker", "inspect", "--format", "{{json .}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Error inspecting container %s: %v", containerID, err)
		return
	}
	
	var containerData map[string]interface{}
	if err := json.Unmarshal(output, &containerData); err != nil {
		log.Printf("Error parsing container data: %v", err)
		return
	}
	
	// Check if container has any port bindings
	hostConfig, ok := containerData["HostConfig"].(map[string]interface{})
	if !ok {
		return
	}
	
	portBindings, ok := hostConfig["PortBindings"].(map[string]interface{})
	if !ok || len(portBindings) == 0 {
		// No port bindings to manage
		return
	}
	
	log.Printf("Container %s has port bindings, checking for conflicts", containerID)
	
	// Check if all ports are in our dynamic range
	allInDynamicRange := true
	for _, bindings := range portBindings {
		bindingsArray, ok := bindings.([]interface{})
		if !ok || len(bindingsArray) == 0 {
			continue
		}
		
		binding := bindingsArray[0].(map[string]interface{})
		hostPort, ok := binding["HostPort"].(string)
		if !ok || hostPort == "" {
			continue
		}
		
		portInt, err := strconv.Atoi(hostPort)
		if err != nil || portInt < s.portRangeMin || portInt > s.portRangeMax {
			allInDynamicRange = false
			break
		}
	}
	
	// If all ports are already in our dynamic range, just mark as processed
	if allInDynamicRange && len(portBindings) > 0 {
		log.Printf("Container %s already has all ports in dynamic range, marking as processed", containerID)
		s.addDynamicPortLabel(containerID)
		// Refresh containers to update our view
		if err := s.refreshContainers(); err != nil {
			log.Printf("Error refreshing containers: %v", err)
		}
		return
	}
	
	// If container is already running with port bindings, check each port
	needsRestart := false
	portsToRemap := make(map[string]string)  // containerPort:protocol -> newHostPort
	
	for containerPortProto, bindings := range portBindings {
		bindingsArray, ok := bindings.([]interface{})
		if !ok || len(bindingsArray) == 0 {
			continue
		}
		
		binding := bindingsArray[0].(map[string]interface{})
		hostPort, ok := binding["HostPort"].(string)
		if !ok || hostPort == "" {
			continue
		}
		
		// Split containerPort:protocol
		parts := strings.Split(containerPortProto, "/")
		if len(parts) != 2 {
			continue
		}
		// We only need the protocol here
		protocol := parts[1]
		
		// Always check if we need to remap
		needsRemap, newPort := s.checkPortCollision(containerID, hostPort, protocol)
		if needsRemap {
			log.Printf("Found port conflict for %s: %s/%s -> %s", 
				containerID, hostPort, protocol, newPort)
			portsToRemap[containerPortProto] = newPort
			needsRestart = true
		}
	}
	
	// If we need to remap any ports, restart the container
	if needsRestart {
		log.Printf("Restarting container %s with remapped ports", containerID)
		
		// For each port that needs remapping, call remapContainerPort
		for containerPortProto, newHostPort := range portsToRemap {
			parts := strings.Split(containerPortProto, "/")
			containerPort := parts[0]
			protocol := parts[1]
			
			// Get current host port from port bindings
			bindings := portBindings[containerPortProto].([]interface{})
			binding := bindings[0].(map[string]interface{})
			oldHostPort := binding["HostPort"].(string)
			
			if err := s.remapContainerPort(containerID, oldHostPort, newHostPort, containerPort, protocol); err != nil {
				log.Printf("Failed to remap port for container %s: %v", containerID, err)
			}
		}
	} else {
		log.Printf("No port conflicts found for container %s, marking as processed", containerID)
		// Mark container as processed even if no remapping was needed
		s.addDynamicPortLabel(containerID)
	}
	
	// Refresh all containers to update our state
	if err := s.refreshContainers(); err != nil {
		log.Printf("Error refreshing containers: %v", err)
	}
}

// handleContainerStop processes a container stop event
func (s *ContainerStore) handleContainerStop(containerID string) {
	log.Printf("Container stop/remove event for: %s", containerID)
	
	// First check if container still exists before doing cleanup
	// This helps distinguish between stop (container still exists) and remove (container gone)
	checkCmd := exec.Command("docker", "inspect", "--format", "{{.ID}}", containerID)
	containerExists := checkCmd.Run() == nil
	
	// Removing container from all maps immediately
	s.mu.Lock()
	delete(s.portMappings, containerID)
	delete(s.containers, containerID)
	delete(s.processedContainers, containerID)
	s.mu.Unlock()
	
	// If the container still exists (just stopped), we'll pick it up again in refresh
	// If it's gone (removed), we've already deleted it from our state
	
	// Always refresh containers to update our state with what Docker now has
	if err := s.refreshContainers(); err != nil {
		log.Printf("Error refreshing containers: %v", err)
	}
	
	// If container was actually removed, log a confirmation of cleanup
	if !containerExists {
		log.Printf("Container %s was removed, cleaned up from all state maps", containerID)
	}
}

// GetContainers returns a copy of all containers
func (s *ContainerStore) GetContainers() []Container {
	s.mu.RLock()
	defer s.mu.RUnlock()

	containers := make([]Container, 0, len(s.containers))
	for _, c := range s.containers {
		containers = append(containers, c)
	}

	return containers
}

// GetContainersByComposeProject groups containers by their Docker Compose project
func (s *ContainerStore) GetContainersByComposeProject() map[string][]Container {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	// First pass: build a map to group containers by their actual projects
	projectsByID := make(map[string]string)
	
	// Find actual project names first and map container IDs to them
	for id, container := range s.containers {
		// Skip containers with no project
		if container.ComposeProject != "" && container.ComposeProject != "<no value>" {
			projectsByID[id] = container.ComposeProject
		}
	}
	
	// Second pass: try to infer missing projects from names, network connect events, etc.
	for id, container := range s.containers {
		// If this container already has a project assigned, skip it
		if _, exists := projectsByID[id]; exists {
			continue
		}
		
		// Try to infer project from container name
		name := container.Names
		// Remove any leading slash
		name = strings.TrimPrefix(name, "/")
		
		// Pattern for docker-compose containers: project_service_1
		parts := strings.Split(name, "_")
		if len(parts) >= 2 {
			// First part is likely the project name
			projectName := parts[0]
			if projectName != "" {
				projectsByID[id] = projectName
				continue
			}
		}
	}
	
	// Final pass: build the actual projects map
	projects := make(map[string][]Container)
	
	for id, container := range s.containers {
		var projectName string
		
		// Get project from our mapping if it exists
		if project, exists := projectsByID[id]; exists {
			projectName = project
		} else {
			// Fallback to standalone
			projectName = "standalone"
		}
		
		// Create project slice if it doesn't exist
		if _, exists := projects[projectName]; !exists {
			projects[projectName] = make([]Container, 0)
		}
		
		projects[projectName] = append(projects[projectName], container)
	}
	
	return projects
}

// RefreshContainers forces a refresh of all containers
func (s *ContainerStore) RefreshContainers() error {
	return s.refreshContainers()
}

// Close stops the event listener and cleans up resources
func (s *ContainerStore) Close() {
	close(s.done)
	if s.eventCmd != nil && s.eventCmd.Process != nil {
		s.eventCmd.Process.Kill()
	}
}

// CheckComposePortConflicts checks for port conflicts within a Docker Compose project
// before containers are started, so we can remap them proactively
func (s *ContainerStore) CheckComposePortConflicts(composeFile string) (map[string]string, error) {
	// Parse the compose file to extract port mappings
	cmd := exec.Command("docker-compose", "-f", composeFile, "config")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to parse compose file: %v", err)
	}

	// Parse YAML output
	var composeConfig map[string]interface{}
	if err := yaml.Unmarshal(output, &composeConfig); err != nil {
		return nil, fmt.Errorf("failed to parse compose config: %v", err)
	}

	// Extract services
	services, ok := composeConfig["services"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid compose file format: no services defined")
	}

	// Map to store port remappings: "service:port" -> "new port"
	portRemappings := make(map[string]string)

	// Check each service for port mappings
	for serviceName, serviceConfig := range services {
		serviceMap, ok := serviceConfig.(map[string]interface{})
		if !ok {
			continue
		}

		ports, ok := serviceMap["ports"].([]interface{})
		if !ok {
			continue
		}

		// Check each port mapping
		for _, portMapping := range ports {
			var hostPort, containerPort, protocol string

			// Handle different port mapping formats
			switch pm := portMapping.(type) {
			case string:
				// Format: "8080:80" or "8080:80/tcp"
				parts := strings.Split(pm, ":")
				if len(parts) == 2 {
					hostPort = parts[0]
					containerPortProto := strings.Split(parts[1], "/")
					containerPort = containerPortProto[0]
					if len(containerPortProto) > 1 {
						protocol = containerPortProto[1]
					} else {
						protocol = "tcp" // Default protocol
					}
				}
			case map[string]interface{}:
				// Format: {published: 8080, target: 80, protocol: tcp}
				if published, ok := pm["published"].(string); ok {
					hostPort = published
				} else if published, ok := pm["published"].(int); ok {
					hostPort = strconv.Itoa(published)
				}

				if target, ok := pm["target"].(string); ok {
					containerPort = target
				} else if target, ok := pm["target"].(int); ok {
					containerPort = strconv.Itoa(target)
				}

				if proto, ok := pm["protocol"].(string); ok {
					protocol = proto
				} else {
					protocol = "tcp" // Default protocol
				}
			}

			if hostPort == "" || containerPort == "" {
				continue
			}

			// Check for collisions
			portInt, err := strconv.Atoi(hostPort)
			if err != nil {
				continue
			}

			// Check if this port is already in use
			inUse := false
			for _, container := range s.containers {
				for _, mapping := range container.PortMappings {
					existingPort, _ := strconv.Atoi(mapping.HostPort)
					if existingPort == portInt && mapping.Protocol == protocol {
						inUse = true
						break
					}
				}
				if inUse {
					break
				}
			}

			// Also check if the port is in use by non-Docker processes
			if !inUse && !s.isPortAvailable(portInt) {
				inUse = true
			}

			// If port is in use, allocate a new one
			if inUse {
				newPort := s.allocateRandomPort()
				portRemappings[fmt.Sprintf("%s:%s", serviceName, hostPort)] = strconv.Itoa(newPort)
				log.Printf("Port conflict detected for service %s: %s -> %d", 
					serviceName, hostPort, newPort)
			}
		}
	}

	return portRemappings, nil
}

// GenerateRemappedComposeFile creates a new Docker Compose file with remapped ports
func (s *ContainerStore) GenerateRemappedComposeFile(originalFile string, remappings map[string]string) (string, error) {
	// Read the original compose file
	origContent, err := os.ReadFile(originalFile)
	if err != nil {
		return "", fmt.Errorf("failed to read compose file: %v", err)
	}

	// Parse YAML
	var composeConfig map[string]interface{}
	if err := yaml.Unmarshal(origContent, &composeConfig); err != nil {
		return "", fmt.Errorf("failed to parse compose file: %v", err)
	}

	// Get services
	services, ok := composeConfig["services"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid compose file format: no services defined")
	}

	// Apply port remappings
	for servicePortKey, newPort := range remappings {
		parts := strings.Split(servicePortKey, ":")
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]
		oldPort := parts[1]

		// Get the service
		serviceConfig, ok := services[serviceName].(map[string]interface{})
		if !ok {
			continue
		}

		// Get ports
		ports, ok := serviceConfig["ports"].([]interface{})
		if !ok {
			continue
		}

		// Replace the port in each mapping
		for i, portMapping := range ports {
			switch pm := portMapping.(type) {
			case string:
				// Format: "8080:80" or "8080:80/tcp"
				if strings.HasPrefix(pm, oldPort+":") {
					// Replace the host port
					parts := strings.Split(pm, ":")
					if len(parts) == 2 {
						ports[i] = newPort + ":" + parts[1]
					}
				}
			case map[string]interface{}:
				// Format: {published: 8080, target: 80, protocol: tcp}
				publishedStr, ok := pm["published"].(string)
				if ok && publishedStr == oldPort {
					pm["published"] = newPort
				}
				publishedInt, ok := pm["published"].(int)
				if ok && strconv.Itoa(publishedInt) == oldPort {
					newPortInt, _ := strconv.Atoi(newPort)
					pm["published"] = newPortInt
				}
			}
		}

		// Update the service config
		serviceConfig["ports"] = ports
		services[serviceName] = serviceConfig
	}

	// Update the compose config
	composeConfig["services"] = services

	// Generate the new YAML
	newContent, err := yaml.Marshal(composeConfig)
	if err != nil {
		return "", fmt.Errorf("failed to generate updated compose file: %v", err)
	}

	// Create a temporary file for the new compose config
	tmpFile, err := os.CreateTemp("", "dynamic-port-mapper-compose-*.yml")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %v", err)
	}

	// Write the new content
	if _, err := tmpFile.Write(newContent); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write updated compose file: %v", err)
	}

	tmpFile.Close()
	return tmpFile.Name(), nil
} 
