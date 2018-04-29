package test

import (
	"testing"
	"time"
	"fmt"
	"github.com/hashicorp/consul/api"
	"errors"
	"github.com/gruntwork-io/terratest/modules/test-structure"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/retry"
	"github.com/gruntwork-io/terratest/modules/logger"
)

const REPO_ROOT = "../"
const CONSUL_CLUSTER_EXAMPLE_VAR_AMI_ID = "ami_id"
const CONSUL_CLUSTER_EXAMPLE_VAR_AWS_REGION = "aws_region"
const CONSUL_CLUSTER_EXAMPLE_VAR_CLUSTER_NAME = "cluster_name"
const CONSUL_CLUSTER_EXAMPLE_VAR_NUM_SERVERS = "num_servers"
const CONSUL_CLUSTER_EXAMPLE_VAR_NUM_CLIENTS = "num_clients"

const CONSUL_CLUSTER_EXAMPLE_DEFAULT_NUM_SERVERS = 3
const CONSUL_CLUSTER_EXAMPLE_DEFAULT_NUM_CLIENTS = 6

const CONSUL_CLUSTER_EXAMPLE_OUTPUT_SERVER_ASG_NAME = "asg_name_servers"
const CONSUL_CLUSTER_EXAMPLE_OUTPUT_CLIENT_ASG_NAME = "asg_name_clients"

const CONSUL_AMI_EXAMPLE_PATH = "../examples/consul-ami/consul.json"

const SAVED_AWS_REGION = "AwsRegion"

// Test the consul-cluster example by:
//
// 1. Copying the code in this repo to a temp folder so tests on the Terraform code can run in parallel without the
//    state files overwriting each other.
// 2. Building the AMI in the consul-ami example with the given build name
// 3. Deploying that AMI using the consul-cluster Terraform code
// 4. Checking that the Consul cluster comes up within a reasonable time period and can respond to requests
func runConsulClusterTest(t *testing.T, packerBuildName string) {
	exampleFolder := test_structure.CopyTerraformFolderToTemp(t, REPO_ROOT, ".")

	test_structure.RunTestStage(t, "setup_ami", func() {
		awsRegion := aws.GetRandomRegion(t, nil, nil)
		amiId := buildAmi(t, CONSUL_AMI_EXAMPLE_PATH, packerBuildName, awsRegion)

		test_structure.SaveAmiId(t, exampleFolder, amiId)
		test_structure.SaveString(t, exampleFolder, SAVED_AWS_REGION, awsRegion)
	})

	defer test_structure.RunTestStage(t, "teardown", func() {
		terraformOptions := test_structure.LoadTerraformOptions(t, exampleFolder)
		terraform.Destroy(t, terraformOptions)

		amiId := test_structure.LoadAmiId(t, exampleFolder)
		awsRegion := test_structure.LoadString(t, exampleFolder, SAVED_AWS_REGION)
		aws.DeleteAmi(t, awsRegion, amiId)
	})

	test_structure.RunTestStage(t, "deploy", func() {
		uniqueId := random.UniqueId()
		awsRegion := test_structure.LoadString(t, exampleFolder, SAVED_AWS_REGION)
		amiId := test_structure.LoadAmiId(t, exampleFolder)

		terraformOptions := &terraform.Options{
			TerraformDir: exampleFolder,
			Vars: map[string]interface{}{
				CONSUL_CLUSTER_EXAMPLE_VAR_AWS_REGION:   awsRegion,
				CONSUL_CLUSTER_EXAMPLE_VAR_CLUSTER_NAME: uniqueId,
				CONSUL_CLUSTER_EXAMPLE_VAR_NUM_SERVERS:  CONSUL_CLUSTER_EXAMPLE_DEFAULT_NUM_SERVERS,
				CONSUL_CLUSTER_EXAMPLE_VAR_NUM_CLIENTS:  CONSUL_CLUSTER_EXAMPLE_DEFAULT_NUM_CLIENTS,
				CONSUL_CLUSTER_EXAMPLE_VAR_AMI_ID:       amiId,
			},
		}

		terraform.InitAndApply(t, terraformOptions)

		test_structure.SaveTerraformOptions(t, exampleFolder, terraformOptions)
	})

	test_structure.RunTestStage(t,"validate", func() {
		awsRegion := test_structure.LoadString(t, exampleFolder, SAVED_AWS_REGION)
		terraformOptions := test_structure.LoadTerraformOptions(t, exampleFolder)

		// Check the Consul servers
		checkConsulClusterIsWorking(t, CONSUL_CLUSTER_EXAMPLE_OUTPUT_SERVER_ASG_NAME, terraformOptions, awsRegion)

		// Check the Consul clients
		checkConsulClusterIsWorking(t, CONSUL_CLUSTER_EXAMPLE_OUTPUT_CLIENT_ASG_NAME, terraformOptions, awsRegion)
	})
}

// Check that the Consul cluster comes up within a reasonable time period and can respond to requests
func checkConsulClusterIsWorking(t *testing.T, asgNameOutputVar string, terratestOptions *terraform.Options, awsRegion string) {
	asgName := terraform.OutputRequired(t, terratestOptions, asgNameOutputVar)
	nodeIpAddress := getIpAddressOfAsgInstance(t, asgName, awsRegion)
	testConsulCluster(t, nodeIpAddress)
}

// Use a Consul client to connect to the given node and use it to verify that:
//
// 1. The Consul cluster has deployed
// 2. The cluster has the expected number of members
// 3. The cluster has elected a leader
func testConsulCluster(t *testing.T, nodeIpAddress string) {
	consulClient := createConsulClient(t, nodeIpAddress)
	maxRetries := 60
	sleepBetweenRetries := 10 * time.Second
	expectedMembers := CONSUL_CLUSTER_EXAMPLE_DEFAULT_NUM_CLIENTS + CONSUL_CLUSTER_EXAMPLE_DEFAULT_NUM_SERVERS

	leader := retry.DoWithRetry(t, "Check Consul members", maxRetries, sleepBetweenRetries, func() (string, error) {
		members, err := consulClient.Agent().Members(false)
		if err != nil {
			return "", err
		}

		if len(members) != expectedMembers {
			return "", fmt.Errorf("Expected the cluster to have %d members, but found %d", expectedMembers, len(members))
		}

		leader, err := consulClient.Status().Leader()
		if err != nil {
			return "", err
		}

		if leader == "" {
			return "", errors.New("Consul cluster returned an empty leader response, so a leader must not have been elected yet.")
		}

		return leader, nil
	})

	logger.Logf(t, "Consul cluster is properly deployed and has elected leader %s", leader)
}

// Create a Consul client
func createConsulClient(t *testing.T, ipAddress string) *api.Client {
	config := api.DefaultConfig()
	config.Address = fmt.Sprintf("%s:8500", ipAddress)
	config.HttpClient.Timeout = 5 * time.Second

	client, err := api.NewClient(config)
	if err != nil {
		t.Fatalf("Failed to create Consul client due to error: %v", err)
	}

	return client
}
