package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type NodeInfo struct {
	Name         string
	Status       string
	Age          string
	Version      string
	InstanceID   string
	InstanceType string
	ASG          string
	ASGCapacity  string
	Taints       string
	CPUCapacity  *resource.Quantity
	CPURequested *resource.Quantity
	MemCapacity  *resource.Quantity
	MemRequested *resource.Quantity
	PodCount     int
}

func main() {
	var outputFormat string
	var showVersion bool
	var openBrowser bool
	var openASG bool
	
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] [NODE_NAME]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "A kubectl plugin that extends 'kubectl get nodes' with AWS EC2 instance information.\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s                           # List all nodes with basic info\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -o wide                   # List all nodes with ASG info\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -o top                    # List nodes with resource usage\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --open ip-10-0-1-100      # Open AWS console for specific node\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --open-asg ip-10-0-1-100  # Open ASG console for specific node\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
	
	flag.StringVar(&outputFormat, "o", "", "Output format. Supported: wide, top")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	flag.BoolVar(&openBrowser, "open", false, "Open AWS console for the specified node")
	flag.BoolVar(&openASG, "open-asg", false, "Open Auto Scaling Group console for the specified node")
	flag.Parse()

	if showVersion {
		fmt.Printf("kubectl-aws-nodes version %s, commit %s, built at %s\n", version, commit, date)
		return
	}

	// Check if node name is specified with --open or --open-asg
	args := flag.Args()
	if openBrowser || openASG {
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "Error: --open and --open-asg require a node name\n")
			os.Exit(1)
		}
		if openBrowser {
			openNodeInBrowser(args[0])
		} else {
			openNodeASGInBrowser(args[0])
		}
		return
	}

	if outputFormat != "" && outputFormat != "wide" && outputFormat != "top" {
		fmt.Fprintf(os.Stderr, "Error: unsupported output format '%s'. Supported: wide, top\n", outputFormat)
		os.Exit(1)
	}
	// Initialize Kubernetes client
	kubeConfig, err := getKubeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting kubeconfig: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating clientset: %v\n", err)
		os.Exit(1)
	}

	// Initialize AWS clients only if needed
	var ec2Client *ec2.Client
	var asgClient *autoscaling.Client
	if outputFormat == "wide" {
		awsConfig, err := awsconfig.LoadDefaultConfig(context.TODO())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading AWS config: %v\n", err)
			os.Exit(1)
		}
		ec2Client = ec2.NewFromConfig(awsConfig)
		asgClient = autoscaling.NewFromConfig(awsConfig)
	}

	// Get nodes
	nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing nodes: %v\n", err)
		os.Exit(1)
	}

	// Get pods for resource calculations
	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing pods: %v\n", err)
		os.Exit(1)
	}

	// Calculate resource usage per node
	nodeResources := make(map[string]*NodeInfo)
	for _, node := range nodes.Items {
		nodeResources[node.Name] = &NodeInfo{
			CPUCapacity:  node.Status.Allocatable.Cpu(),
			CPURequested: resource.NewQuantity(0, resource.DecimalSI),
			MemCapacity:  node.Status.Allocatable.Memory(),
			MemRequested: resource.NewQuantity(0, resource.BinarySI),
			PodCount:     0,
		}
	}

	for _, pod := range pods.Items {
		if nodeInfo, exists := nodeResources[pod.Spec.NodeName]; exists {
			nodeInfo.PodCount++
			for _, container := range pod.Spec.Containers {
				if cpu := container.Resources.Requests.Cpu(); cpu != nil {
					nodeInfo.CPURequested.Add(*cpu)
				}
				if mem := container.Resources.Requests.Memory(); mem != nil {
					nodeInfo.MemRequested.Add(*mem)
				}
			}
		}
	}

	// Get EC2 instances and ASG info only for wide format
	var instanceMap map[string]types.Instance
	var asgMap map[string]string
	if outputFormat == "wide" {
		var err error
		instanceMap, err = getEC2Instances(ec2Client)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting EC2 instances: %v\n", err)
			os.Exit(1)
		}
		
		asgMap, err = getASGCapacities(asgClient)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting ASG capacities: %v\n", err)
			os.Exit(1)
		}
	}

	// Print results
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if outputFormat == "wide" {
		fmt.Fprintln(w, "NAME\tSTATUS\tAGE\tVERSION\tINSTANCE-ID\tINSTANCE-TYPE\tTAINTS\tASG\tASG-CAPACITY")
	} else if outputFormat == "top" {
		fmt.Fprintln(w, "NAME\tPODS\tCPU-CAP\tCPU-REQ\tCPU-FREE%\tMEM-CAP\tMEM-REQ\tMEM-FREE%")
	} else {
		fmt.Fprintln(w, "NAME\tSTATUS\tAGE\tVERSION\tINSTANCE-ID\tINSTANCE-TYPE\tTAINTS")
	}

	for _, node := range nodes.Items {
		nodeInfo := NodeInfo{
			Name:    node.Name,
			Status:  getNodeStatus(node),
			Age:     getNodeAge(node),
			Version: node.Status.NodeInfo.KubeletVersion,
			Taints:  getNodeTaints(node),
		}

		// Copy resource info
		if resInfo, exists := nodeResources[node.Name]; exists {
			nodeInfo.CPUCapacity = resInfo.CPUCapacity
			nodeInfo.CPURequested = resInfo.CPURequested
			nodeInfo.MemCapacity = resInfo.MemCapacity
			nodeInfo.MemRequested = resInfo.MemRequested
			nodeInfo.PodCount = resInfo.PodCount
		}

		// Get instance info from Kubernetes
		nodeInfo.InstanceID = getInstanceID(node)
		nodeInfo.InstanceType = getInstanceType(node)
		
		// Get ASG info from AWS (only if we have AWS access and instance ID)
		if nodeInfo.InstanceID != "" {
			if instance, exists := instanceMap[nodeInfo.InstanceID]; exists {
				nodeInfo.ASG = getASGFromTags(instance.Tags)
				if nodeInfo.ASG != "" {
					if capacity, exists := asgMap[nodeInfo.ASG]; exists {
						nodeInfo.ASGCapacity = capacity
					}
				}
			}
		}

		if outputFormat == "wide" {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				nodeInfo.Name, nodeInfo.Status, nodeInfo.Age,
				nodeInfo.Version, nodeInfo.InstanceID, nodeInfo.InstanceType, nodeInfo.Taints, nodeInfo.ASG, nodeInfo.ASGCapacity)
		} else if outputFormat == "top" {
			cpuFree := calculateFreePercentage(nodeInfo.CPUCapacity, nodeInfo.CPURequested)
			memFree := calculateFreePercentage(nodeInfo.MemCapacity, nodeInfo.MemRequested)
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%.1f%%\t%s\t%s\t%.1f%%\n",
				nodeInfo.Name, nodeInfo.PodCount,
				formatResource(nodeInfo.CPUCapacity), formatResource(nodeInfo.CPURequested), cpuFree,
				formatMemory(nodeInfo.MemCapacity), formatMemory(nodeInfo.MemRequested), memFree)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				nodeInfo.Name, nodeInfo.Status, nodeInfo.Age,
				nodeInfo.Version, nodeInfo.InstanceID, nodeInfo.InstanceType, nodeInfo.Taints)
		}
	}

	w.Flush()
}

func getKubeConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}

func getEC2Instances(client *ec2.Client) (map[string]types.Instance, error) {
	result, err := client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{})
	if err != nil {
		return nil, err
	}

	instanceMap := make(map[string]types.Instance)
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				instanceMap[*instance.InstanceId] = instance
			}
		}
	}
	return instanceMap, nil
}

func getInstanceType(node v1.Node) string {
	if instanceType, exists := node.Labels["node.kubernetes.io/instance-type"]; exists {
		return instanceType
	}
	return ""
}

func getInstanceID(node v1.Node) string {
	// Extract instance ID from spec.providerID (format: aws:///zone/instance-id)
	if node.Spec.ProviderID != "" {
		parts := strings.Split(node.Spec.ProviderID, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

func getNodeStatus(node v1.Node) string {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady {
			if condition.Status == v1.ConditionTrue {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

func getNodeAge(node v1.Node) string {
	age := time.Since(node.CreationTimestamp.Time)
	days := int(age.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(age.Hours())
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", int(age.Minutes()))
}

func getNodeTaints(node v1.Node) string {
	var taintKeys []string
	for _, taint := range node.Spec.Taints {
		taintKeys = append(taintKeys, taint.Key)
	}
	return strings.Join(taintKeys, ",")
}

func getASGFromTags(tags []types.Tag) string {
	for _, tag := range tags {
		if tag.Key != nil && *tag.Key == "aws:autoscaling:groupName" && tag.Value != nil {
			return *tag.Value
		}
	}
	return ""
}

func calculateFreePercentage(capacity, requested *resource.Quantity) float64 {
	if capacity == nil || capacity.IsZero() {
		return 0
	}
	capVal := capacity.MilliValue()
	reqVal := int64(0)
	if requested != nil {
		reqVal = requested.MilliValue()
	}
	return float64(capVal-reqVal) / float64(capVal) * 100
}

func formatResource(q *resource.Quantity) string {
	if q == nil {
		return "0"
	}
	return q.String()
}

func formatMemory(q *resource.Quantity) string {
	if q == nil {
		return "0"
	}

	bytes := q.Value()

	// Convert to largest unit >= 1
	if bytes >= 1024*1024*1024*1024 { // Ti
		return fmt.Sprintf("%.1fTi", float64(bytes)/(1024*1024*1024*1024))
	}
	if bytes >= 1024*1024*1024 { // Gi
		return fmt.Sprintf("%.1fGi", float64(bytes)/(1024*1024*1024))
	}
	if bytes >= 1024*1024 { // Mi
		return fmt.Sprintf("%.1fMi", float64(bytes)/(1024*1024))
	}
	if bytes >= 1024 { // Ki
		return fmt.Sprintf("%.1fKi", float64(bytes)/1024)
	}

	return fmt.Sprintf("%d", bytes)
}

func getASGCapacities(client *autoscaling.Client) (map[string]string, error) {
	result, err := client.DescribeAutoScalingGroups(context.TODO(), &autoscaling.DescribeAutoScalingGroupsInput{})
	if err != nil {
		return nil, err
	}

	asgMap := make(map[string]string)
	for _, asg := range result.AutoScalingGroups {
		if asg.AutoScalingGroupName != nil {
			capacity := fmt.Sprintf("%d/%d/%d", 
				*asg.MinSize, 
				*asg.MaxSize, 
				*asg.DesiredCapacity)
			asgMap[*asg.AutoScalingGroupName] = capacity
		}
	}

	return asgMap, nil
}

func openNodeInBrowser(nodeName string) {
	// Get Kubernetes client
	config, err := getKubeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting kubeconfig: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating clientset: %v\n", err)
		os.Exit(1)
	}

	// Get the specific node
	node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting node '%s': %v\n", nodeName, err)
		os.Exit(1)
	}

	// Get instance ID from node
	instanceID := getInstanceID(*node)
	if instanceID == "" {
		fmt.Fprintf(os.Stderr, "Error: Could not find instance ID for node '%s'\n", nodeName)
		os.Exit(1)
	}

	// Initialize AWS client to get region
	awsConfig, err := awsconfig.LoadDefaultConfig(context.TODO())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading AWS config: %v\n", err)
		os.Exit(1)
	}

	// Build AWS console URL
	region := awsConfig.Region
	url := fmt.Sprintf("https://%s.console.aws.amazon.com/ec2/home?region=%s#InstanceDetails:instanceId=%s", 
		region, region, instanceID)

	fmt.Printf("Opening AWS console for node '%s' (instance: %s)...\n", nodeName, instanceID)
	
	// Open browser
	err = openURL(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening browser: %v\n", err)
		fmt.Printf("Please open this URL manually: %s\n", url)
		os.Exit(1)
	}
}

func openNodeASGInBrowser(nodeName string) {
	// Get Kubernetes client
	config, err := getKubeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting kubeconfig: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating clientset: %v\n", err)
		os.Exit(1)
	}

	// Get the specific node
	node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting node '%s': %v\n", nodeName, err)
		os.Exit(1)
	}

	// Get instance ID from node
	instanceID := getInstanceID(*node)
	if instanceID == "" {
		fmt.Fprintf(os.Stderr, "Error: Could not find instance ID for node '%s'\n", nodeName)
		os.Exit(1)
	}

	// Initialize AWS clients
	awsConfig, err := awsconfig.LoadDefaultConfig(context.TODO())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading AWS config: %v\n", err)
		os.Exit(1)
	}

	ec2Client := ec2.NewFromConfig(awsConfig)

	// Get EC2 instance to find ASG
	instanceMap, err := getEC2Instances(ec2Client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting EC2 instances: %v\n", err)
		os.Exit(1)
	}

	instance, exists := instanceMap[instanceID]
	if !exists {
		fmt.Fprintf(os.Stderr, "Error: Could not find EC2 instance '%s'\n", instanceID)
		os.Exit(1)
	}

	// Get ASG name from instance tags
	asgName := getASGFromTags(instance.Tags)
	if asgName == "" {
		fmt.Fprintf(os.Stderr, "Error: Could not find Auto Scaling Group for node '%s'\n", nodeName)
		os.Exit(1)
	}

	// Build AWS console URL for ASG
	region := awsConfig.Region
	url := fmt.Sprintf("https://%s.console.aws.amazon.com/ec2/home?region=%s#AutoScalingGroupDetails:id=%s", 
		region, region, asgName)

	fmt.Printf("Opening ASG console for node '%s' (ASG: %s)...\n", nodeName, asgName)
	
	// Open browser
	err = openURL(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening browser: %v\n", err)
		fmt.Printf("Please open this URL manually: %s\n", url)
		os.Exit(1)
	}
}

func openURL(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
